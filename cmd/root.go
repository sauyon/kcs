package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/FogDong/kcs/internal/parser"
	"github.com/FogDong/kcs/internal/scanner"
	"github.com/FogDong/kcs/internal/selector"
	"github.com/FogDong/kcs/internal/switcher"
)

var (
	listFlag      bool
	currentFlag   bool
	dirFlag       string
	sessionFlag   bool
	noSessionFlag bool
)

var rootCmd = &cobra.Command{
	Use:   "kcs [search]",
	Short: "Kubernetes Config Switcher",
	Long:  `kcs helps you manage multiple kubeconfig files and contexts in ~/.kube/`,
	Args:  cobra.MaximumNArgs(1),
	Run:   run,
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize kcs and show shell configuration",
	Long:  `Shows the command to add to your shell configuration (.zshrc or .bashrc) to use kcs.`,
	Run:   runInit,
}

func init() {
	rootCmd.Flags().BoolVarP(&listFlag, "list", "l", false, "List all contexts without interactive selection")
	rootCmd.Flags().BoolVarP(&currentFlag, "current", "c", false, "Show current context")
	rootCmd.Flags().StringVarP(&dirFlag, "dir", "d", "", "Custom kubeconfig directory (default: ~/.kube)")
	rootCmd.Flags().BoolVarP(&sessionFlag, "session", "s", false, "Enable session mode (overrides KCS_SESSION)")
	rootCmd.Flags().BoolVar(&noSessionFlag, "no-session", false, "Disable session mode (overrides KCS_SESSION)")
	rootCmd.AddCommand(initCmd)
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func sessionModeEnabled() bool {
	if noSessionFlag {
		return false
	}
	return sessionFlag || os.Getenv("KCS_SESSION") != ""
}

// kubeDir returns the directory used to place the kcs-config symlink.
// Always ~/.kube unless --dir is set.
func kubeDir() (string, error) {
	if dirFlag != "" {
		return dirFlag, nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(homeDir, ".kube"), nil
}

// kubeconfigFiles returns the list of kubeconfig files to source contexts from.
// Priority: --dir flag > KUBECONFIG env var > ~/.kube/ directory scan.
func kubeconfigFiles(dir string) ([]string, error) {
	if dirFlag != "" {
		return scanner.Scan(dir)
	}
	if kc := os.Getenv("KUBECONFIG"); kc != "" {
		var files []string
		for _, p := range filepath.SplitList(kc) {
			if p != "" {
				files = append(files, p)
			}
		}
		if len(files) > 0 {
			return files, nil
		}
	}
	return scanner.Scan(dir)
}

// selectContext collects kubeconfig files, parses contexts, and runs interactive selection.
func selectContext(args []string) (parser.ContextInfo, error) {
	dir, err := kubeDir()
	if err != nil {
		return parser.ContextInfo{}, err
	}
	files, err := kubeconfigFiles(dir)
	if err != nil {
		return parser.ContextInfo{}, fmt.Errorf("error collecting kubeconfig files: %w", err)
	}
	if len(files) == 0 {
		return parser.ContextInfo{}, fmt.Errorf("no kubeconfig files found")
	}
	var allContexts []parser.ContextInfo
	for _, file := range files {
		contexts, err := parser.Parse(file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: skipping %s: %v\n", file, err)
			continue
		}
		allContexts = append(allContexts, contexts...)
	}
	if len(allContexts) == 0 {
		return parser.ContextInfo{}, fmt.Errorf("no contexts found in any kubeconfig file")
	}
	var searchQuery string
	if len(args) > 0 {
		searchQuery = args[0]
	}
	return selector.Select(allContexts, searchQuery)
}

func run(cmd *cobra.Command, args []string) {
	dir, err := kubeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Handle --current flag
	if currentFlag {
		showCurrentContext(dir)
		return
	}

	// Handle --list flag
	if listFlag {
		files, err := kubeconfigFiles(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error collecting kubeconfig files: %v\n", err)
			os.Exit(1)
		}
		var allContexts []parser.ContextInfo
		for _, file := range files {
			contexts, err := parser.Parse(file)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: skipping %s: %v\n", file, err)
				continue
			}
			allContexts = append(allContexts, contexts...)
		}
		var searchQuery string
		if len(args) > 0 {
			searchQuery = args[0]
		}
		listContexts(allContexts, searchQuery)
		return
	}

	selected, err := selectContext(args)
	if err != nil {
		if err == selector.ErrUserCancelled {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if sessionModeEnabled() {
		if _, err := switcher.SwitchSession(selected); err != nil {
			fmt.Fprintf(os.Stderr, "Error switching context: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "✓ Switched to %s (%s)\n", selected.Name, selected.Cluster)
		return
	}

	if err := switcher.Switch(dir, selected); err != nil {
		fmt.Fprintf(os.Stderr, "Error switching context: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Switched to %s (%s)\n", selected.Name, selected.Cluster)
}

func showCurrentContext(kubeDir string) {
	ctx, file, err := switcher.GetCurrentContext(kubeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%s (kubeconfig: %s)\n", ctx, file)
}

func runInit(cmd *cobra.Command, args []string) {
	if sessionModeEnabled() {
		sessionPath := switcher.SessionPath()
		fmt.Printf("export KUBECONFIG='%s'\n", strings.ReplaceAll(sessionPath, "'", "'\\''"))
		return
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot determine home directory: %v\n", err)
		os.Exit(1)
	}

	kcsConfigPath := homeDir + "/.kube/kcs-config"
	exportCmd := fmt.Sprintf("export KUBECONFIG=%s", kcsConfigPath)

	fmt.Println("Add the following line to your shell configuration file:")
	fmt.Println()
	fmt.Println("For zsh (~/.zshrc):")
	fmt.Printf("  echo '%s' >> ~/.zshrc\n", exportCmd)
	fmt.Println()
	fmt.Println("For bash (~/.bashrc):")
	fmt.Printf("  echo '%s' >> ~/.bashrc\n", exportCmd)
	fmt.Println()
	fmt.Println("Or manually add this line:")
	fmt.Printf("  %s\n", exportCmd)
	fmt.Println()
	fmt.Println("Then reload your shell or run:")
	fmt.Printf("  source ~/.zshrc  # or source ~/.bashrc\n")
}

func listContexts(contexts []parser.ContextInfo, searchQuery string) {
	filtered := contexts
	if searchQuery != "" {
		filtered = selector.Filter(contexts, searchQuery)
	}

	if len(filtered) == 0 {
		fmt.Println("No contexts match the query")
		return
	}

	for _, ctx := range filtered {
		ns := ctx.Namespace
		if ns == "" {
			ns = "default"
		}
		fmt.Printf("[%s] %s / %s (ns: %s)\n",
			ctx.SourceFileName, ctx.Name, ctx.Cluster, ns)
	}
}
