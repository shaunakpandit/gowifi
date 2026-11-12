package main

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

type network struct {
	ssid   string
	signal int
	secure bool
}

type row struct {
	label  string
	header bool
}

func nmcli(arguments ...string) string {
	output, err := exec.Command("nmcli", arguments...).Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "nmcli failed:", err)
		os.Exit(1)
	}
	return strings.TrimSpace(string(output))
}

// parseTerse splits an `nmcli -t` line on unescaped ':' and unescapes `\:` and `\\`.
func parseTerse(line string) []string {
	fields, current, escaped := []string{}, strings.Builder{}, false
	for _, character := range line {
		switch {
		case escaped:
			current.WriteRune(character)
			escaped = false
		case character == '\\':
			escaped = true
		case character == ':':
			fields = append(fields, current.String())
			current.Reset()
		default:
			current.WriteRune(character)
		}
	}
	return append(fields, current.String())
}

func lines(output string) [][]string {
	if output == "" {
		return nil
	}
	rows := [][]string{}
	for _, line := range strings.Split(output, "\n") {
		rows = append(rows, parseTerse(line))
	}
	return rows
}

func status() string {
	for _, fields := range lines(nmcli("-t", "-f", "TYPE,STATE,CONNECTION", "device")) {
		if (fields[0] == "wifi" || fields[0] == "ethernet") && strings.HasPrefix(fields[1], "connected") {
			return fmt.Sprintf("Connected: \033[1m%s\033[0m (%s)", fields[2], fields[0])
		}
	}
	return "Not connected"
}

// parseNetworks dedupes by SSID keeping the strongest signal, sorted strongest first.
func parseNetworks(output string) []network {
	strongest := map[string]network{}
	for _, fields := range lines(output) {
		signal, _ := strconv.Atoi(fields[1])
		candidate := network{fields[0], signal, fields[2] != "" && fields[2] != "--"}
		if existing, seen := strongest[candidate.ssid]; candidate.ssid != "" && (!seen || signal > existing.signal) {
			strongest[candidate.ssid] = candidate
		}
	}
	networks := []network{}
	for _, network := range strongest {
		networks = append(networks, network)
	}
	slices.SortFunc(networks, func(a, b network) int { return b.signal - a.signal })
	return networks
}

// shortcut: assumes connection name == SSID (NetworkManager's default), renamed profiles show as new.
func knownSSIDs() map[string]bool {
	known := map[string]bool{}
	for _, fields := range lines(nmcli("-t", "-f", "NAME,TYPE", "connection", "show")) {
		if fields[1] == "802-11-wireless" {
			known[fields[0]] = true
		}
	}
	return known
}

func bars(signal int) string {
	full := min(4, max(1, (signal+24)/25))
	return []string{"▂", "▂▄", "▂▄▆", "▂▄▆█"}[full-1] + strings.Repeat("_", 4-full)
}

// move steps the cursor by delta, skipping headers; stays put if nothing selectable that way.
func move(rows []row, cursor, delta int) int {
	for next := cursor + delta; next >= 0 && next < len(rows); next += delta {
		if !rows[next].header {
			return next
		}
	}
	return cursor
}

func stty(tty *os.File, arguments ...string) string {
	command := exec.Command("stty", arguments...)
	command.Stdin = tty
	output, _ := command.Output()
	return strings.TrimSpace(string(output))
}

// pick shows an arrow-key menu and returns the chosen row index, or -1 on q/Esc/Ctrl-C.
func pick(rows []row) int {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "no terminal:", err)
		os.Exit(1)
	}
	defer tty.Close()
	saved := stty(tty, "-g")
	stty(tty, "raw", "-echo")
	fmt.Print("\033[?25l")
	defer func() { stty(tty, saved); fmt.Print("\033[?25h") }()

	cursor := move(rows, -1, 1)
	draw := func() {
		for index, row := range rows {
			switch {
			case row.header:
				fmt.Printf("\r\033[2K\033[2m%s\033[0m\r\n", row.label)
			case index == cursor:
				fmt.Printf("\r\033[2K\033[36m❯ %s\033[0m\r\n", row.label)
			default:
				fmt.Printf("\r\033[2K  %s\r\n", row.label)
			}
		}
	}
	draw()
	buffer := make([]byte, 3)
	for {
		count, _ := tty.Read(buffer)
		key := string(buffer[:count])
		switch key {
		case "\033[A", "k":
			cursor = move(rows, cursor, -1)
		case "\033[B", "j":
			cursor = move(rows, cursor, 1)
		case "\r", "\n":
			return cursor
		case "q", "\033", "\x03":
			return -1
		}
		fmt.Printf("\033[%dA", len(rows))
		draw()
	}
}

func newConnection() {
	fmt.Println("Scanning…")
	networks := parseNetworks(nmcli("-t", "-f", "SSID,SIGNAL,SECURITY", "device", "wifi", "list", "--rescan", "yes"))
	fmt.Print("\033[1A\033[2K")
	if len(networks) == 0 {
		fmt.Println("No wifi networks found")
		return
	}
	known := knownSSIDs()
	width := 0
	for _, network := range networks {
		width = max(width, len(network.ssid))
	}
	label := func(network network) string {
		return fmt.Sprintf("%-*s  %s  %s", width, network.ssid, bars(network.signal), map[bool]string{true: "🔒", false: ""}[network.secure])
	}

	rows, choices := []row{}, []network{}
	for _, wantKnown := range []bool{true, false} {
		section := slices.DeleteFunc(slices.Clone(networks), func(network network) bool { return known[network.ssid] != wantKnown })
		if len(section) == 0 {
			continue
		}
		rows = append(rows, row{label: map[bool]string{true: "Known networks", false: "New networks"}[wantKnown], header: true})
		choices = append(choices, network{})
		for _, network := range section {
			rows = append(rows, row{label: label(network)})
			choices = append(choices, network)
		}
	}

	choice := pick(rows)
	if choice < 0 {
		return
	}
	chosen := choices[choice]
	// --ask lets nmcli prompt for the password itself (hidden, and kept off the process list).
	arguments := []string{"--ask", "device", "wifi", "connect", chosen.ssid}
	if known[chosen.ssid] {
		arguments = []string{"--ask", "connection", "up", "id", chosen.ssid}
	}
	command := exec.Command("nmcli", arguments...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if command.Run() != nil {
		os.Exit(1)
	}
}

func main() {
	fmt.Println(status())
	fmt.Println()
	if pick([]row{{label: "new connection"}, {label: "exit"}}) == 0 {
		newConnection()
	}
}
