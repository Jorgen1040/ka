package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/AlecAivazis/survey/v2"
	"golang.org/x/term"
)

const (
	greenBgWhiteText = "\033[1;42;37m"
	resetColor       = "\033[0m"
)

func main() {
	// Command-line flags
	signal := flag.String("s", "", "Signal to send (e.g., -s 9 for SIGKILL)")
	yes := flag.Bool("y", false, "Assume yes; kill all matching processes without confirmation")
	port := flag.String("p", "", "Port to search for (e.g., -p 3000 for processes listening on port 3000)")

	// Pull out bare numeric signals like -9 before flag parsing
	argv, bareSignal, err := preprocessArgs(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	flag.CommandLine.Parse(argv)

	portSet := flagWasSet(flag.CommandLine, "p")
	signalSet := flagWasSet(flag.CommandLine, "s")
	if err := validateSignalForms(bareSignal, signalSet); err != nil {
		log.Fatal(err)
	}

	args := flag.Args()
	if len(args) == 0 && !portSet {
		fmt.Println("Usage: ka [options] process_name")
		fmt.Println("       ka [options] -p port")
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Extract the process name
	var processName string
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			log.Fatalf("Unexpected argument %s: flags must come before the process name", arg)
		}
		processName = arg
	}

	if processName != "" && portSet {
		log.Fatal("Cannot combine -p with a process name")
	}
	if processName == "" && !portSet {
		log.Fatal("Process name or port is required")
	}
	if bareSignal != "" {
		*signal = bareSignal
	}

	// Default signal is SIGTERM (15)
	if *signal == "" {
		*signal = "15"
	}

	// Get the current process ID to exclude it later
	currentPID := os.Getpid()

	// searchTerm is what gets highlighted in the selection list
	searchTerm := processName

	var pids []int
	if portSet {
		portNum, err := strconv.Atoi(*port)
		if err != nil || portNum < 1 || portNum > 65535 {
			log.Fatalf("Invalid port: %s", *port)
		}
		// Normalize forms like "+3000" that Atoi accepts but lsof rejects
		*port = strconv.Itoa(portNum)
		searchTerm = *port
		pids = findPIDsByPort(*port, currentPID)
		if len(pids) == 0 {
			fmt.Printf("No processes found listening on port %s\n", *port)
			os.Exit(0)
		}
	} else {
		pids = findPIDsByName(processName, currentPID)
		if len(pids) == 0 {
			fmt.Printf("No processes found matching '%s'\n", processName)
			os.Exit(0)
		}
	}

	// String form of the PIDs, for passing to ps later
	pidStrings := make([]string, 0, len(pids))
	for _, pid := range pids {
		pidStrings = append(pidStrings, strconv.Itoa(pid))
	}

	// If -y flag is provided, kill all matching processes without confirmation
	if *yes {
		for _, pid := range pids {
			err := exec.Command("kill", "-"+*signal, strconv.Itoa(pid)).Run()
			if err != nil {
				fmt.Printf("Failed to kill process %d: %v\n", pid, err)
			} else {
				fmt.Printf("Killed process %d\n", pid)
			}
		}
		return
	}

	// If only one matching process, kill it without interactive dialog
	if len(pids) == 1 {
		pid := pids[0]
		err := exec.Command("kill", "-"+*signal, strconv.Itoa(pid)).Run()
		if err != nil {
			fmt.Printf("Failed to kill process %d: %v\n", pid, err)
		} else {
			fmt.Printf("Killed process %d\n", pid)
		}
		return
	}

	// Get terminal size
	width, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		// Default to 80x24 if terminal size cannot be determined
		width = 80
		height = 24
	}

	// Adjust PageSize to use full terminal height before scrolling
	pageSize := max(height-4, 1) // Subtract for prompt and padding

	// Prepare options for interactive selection
	pidMap := make(map[string]int)
	var options []string

	// Use ps to get command lines for the PIDs
	psArgs := append([]string{"-o", "pid=,comm=,args=", "-p"}, pidStrings...)
	psOut := commandOutput("ps", psArgs...)

	scanner := bufio.NewScanner(strings.NewReader(psOut))
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pidStr, name, cmdline := fields[0], fields[1], strings.Join(fields[2:], " ")

		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			continue
		}

		// Sanitize name and cmdline to remove newlines
		name = sanitizeString(name)
		cmdline = sanitizeString(cmdline)
		optionStr := formatOptionWithHighlight(pid, name, cmdline, width, searchTerm)
		options = append(options, optionStr)
		pidMap[optionStr] = pid
	}

	if len(options) == 0 {
		if portSet {
			fmt.Printf("No processes found listening on port %s\n", *port)
		} else {
			fmt.Printf("No processes found matching '%s'\n", processName)
		}
		os.Exit(0)
	}

	// Interactive selection
	selectedOptions := []string{}
	prompt := &survey.MultiSelect{
		Message:  "Select processes to kill:",
		Options:  options,
		Default:  options,
		PageSize: pageSize,
	}
	err = survey.AskOne(prompt, &selectedOptions)
	if err != nil {
		log.Fatalf("%v", err)
	}

	// Kill selected processes
	for _, option := range selectedOptions {
		pid := pidMap[option]
		err := exec.Command("kill", "-"+*signal, strconv.Itoa(pid)).Run()
		if err != nil {
			fmt.Printf("Failed to kill process %d: %v\n", pid, err)
		} else {
			fmt.Printf("Killed process %d\n", pid)
		}
	}
}

func preprocessArgs(args []string) ([]string, string, error) {
	argv := make([]string, 0, len(args))
	bareSignal := ""
	optionsEnded := false

	for _, arg := range args {
		if arg == "--" {
			optionsEnded = true
			argv = append(argv, arg)
			continue
		}
		if !optionsEnded {
			if rest, ok := strings.CutPrefix(arg, "-"); ok {
				if num, err := strconv.Atoi(rest); err == nil && num > 0 {
					if bareSignal != "" {
						return nil, "", errors.New("cannot specify more than one bare signal")
					}
					bareSignal = strconv.Itoa(num)
					continue
				}
			}
		}
		argv = append(argv, arg)
	}

	return argv, bareSignal, nil
}

func flagWasSet(flagSet *flag.FlagSet, name string) bool {
	wasSet := false
	flagSet.Visit(func(f *flag.Flag) {
		if f.Name == name {
			wasSet = true
		}
	})
	return wasSet
}

func validateSignalForms(bareSignal string, signalSet bool) error {
	if bareSignal != "" && signalSet {
		return errors.New("cannot combine a bare signal with -s")
	}
	return nil
}

// findPIDsByName uses pgrep to find processes whose command line matches name
func findPIDsByName(name string, excludePID int) []int {
	return parsePIDs(strings.Fields(commandOutput("pgrep", "-f", name)), excludePID)
}

// findPIDsByPort uses lsof to find TCP listeners and UDP sockets on the port
func findPIDsByPort(port string, excludePID int) []int {
	pidStrings := strings.Fields(commandOutput("lsof", "-nP", "-w", "-t", "-iTCP:"+port, "-sTCP:LISTEN"))

	// lsof also matches sockets connected to the port, so keep only local binds
	curPID := ""
	for _, line := range strings.Split(commandOutput("lsof", "-nP", "-w", "-Fpn", "-iUDP:"+port), "\n") {
		if rest, ok := strings.CutPrefix(line, "p"); ok {
			curPID = rest
			continue
		}
		if addr, ok := strings.CutPrefix(line, "n"); ok {
			localAddr, _, _ := strings.Cut(addr, "->")
			if strings.HasSuffix(localAddr, ":"+port) {
				pidStrings = append(pidStrings, curPID)
			}
		}
	}
	return parsePIDs(pidStrings, excludePID)
}

// commandOutput runs a command and returns its stdout; a silent non-zero exit
// means "no matches"
func commandOutput(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			log.Fatalf("Failed to run %s: %v", name, err)
		}
		if len(exitErr.Stderr) > 0 {
			log.Fatalf("%s: %s", name, strings.TrimSpace(string(exitErr.Stderr)))
		}
	}
	return string(out)
}

// parsePIDs converts PID strings to ints, dropping duplicates and excludePID
func parsePIDs(pidStrings []string, excludePID int) []int {
	seen := make(map[int]bool)
	var pids []int
	for _, pidStr := range pidStrings {
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			continue
		}
		if pid == excludePID || seen[pid] {
			continue
		}
		seen[pid] = true
		pids = append(pids, pid)
	}
	return pids
}

func formatOptionWithHighlight(pid int, name, cmdline string, width int, processName string) string {
	pidWidth := 8
	nameWidth := 25
	cmdWidth := max(width-pidWidth-nameWidth-11, 10)

	name = truncateString(name, nameWidth)
	cmdline = truncateString(cmdline, cmdWidth)

	name = highlightText(name, processName)
	cmdline = highlightText(cmdline, processName)

	optionStr := fmt.Sprintf("%-*d  %-*s  %-*s",
		pidWidth, pid,
		nameWidth, name,
		cmdWidth, cmdline)

	return sanitizeString(optionStr)
}

func highlightText(text, search string) string {
	if search == "" {
		return text
	}
	return strings.ReplaceAll(text, search, greenBgWhiteText+search+resetColor)
}

// truncateString truncates a string to a specified width, adding "..." if truncated
func truncateString(s string, maxWidth int) string {
	runes := []rune(s)
	if len(runes) <= maxWidth {
		return s
	}
	if maxWidth > 3 {
		return string(runes[:maxWidth-3]) + ".."
	}
	return string(runes[:maxWidth])
}

// sanitizeString removes any newline characters from a string
func sanitizeString(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " ")
}
