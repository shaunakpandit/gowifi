package main

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Basic 16 ANSI colors only, so the terminal's own theme decides the actual shades.
const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	blue   = "\033[34m"
	cyan   = "\033[36m"
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
			return green + "● " + reset + bold + fields[2] + reset + dim + " (" + fields[0] + ")" + reset
		}
	}
	return red + "○ Not connected" + reset
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
	color := map[int]string{1: red, 2: yellow, 3: green, 4: green}[full]
	return color + []string{"▂", "▂▄", "▂▄▆", "▂▄▆█"}[full-1] + reset + dim + strings.Repeat("_", 4-full) + reset
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

// rawTTY puts the terminal in raw mode (keys arrive unbuffered, Ctrl-C as a byte) and returns a restore func.
func rawTTY() (*os.File, func()) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "no terminal:", err)
		os.Exit(1)
	}
	saved := stty(tty, "-g")
	stty(tty, "raw", "-echo")
	fmt.Print("\033[?25l")
	return tty, func() { stty(tty, saved); fmt.Print("\033[?25h"); tty.Close() }
}

// pick shows an arrow-key menu and returns the chosen row index, or -1 on q/Esc/Ctrl-C.
func pick(rows []row) int {
	tty, restore := rawTTY()
	defer restore()

	cursor := move(rows, -1, 1)
	draw := func() {
		for index, row := range rows {
			switch {
			case row.header:
				fmt.Printf("\r\033[2K%s%s%s%s\r\n", bold, blue, row.label, reset)
			case index == cursor:
				fmt.Printf("\r\033[2K%s%s❯ %s%s\r\n", bold, cyan, row.label, reset)
			default:
				fmt.Printf("\r\033[2K  %s\r\n", row.label)
			}
		}
		fmt.Printf("\r\033[2K%s↑/↓ move · enter select · q quit%s\r\n", dim, reset)
	}
	draw()
	buffer := make([]byte, 3)
	for {
		count, _ := tty.Read(buffer)
		switch string(buffer[:count]) {
		case "\033[A", "k":
			cursor = move(rows, cursor, -1)
		case "\033[B", "j":
			cursor = move(rows, cursor, 1)
		case "\r", "\n":
			return cursor
		case "q", "\033", "\x03":
			return -1
		}
		fmt.Printf("\033[%dA", len(rows)+1)
		draw()
	}
}

// readPassword reads masked input; ok is false on Esc/Ctrl-C or an empty entry.
func readPassword(ssid string) (password string, ok bool) {
	tty, restore := rawTTY()
	defer restore()
	fmt.Printf("\r%sPassword for %s%s%s: ", yellow, bold, ssid, reset)
	input := []byte{}
	buffer := make([]byte, 1)
	for {
		tty.Read(buffer)
		switch character := buffer[0]; {
		case character == '\r' || character == '\n':
			fmt.Print("\r\n")
			return string(input), len(input) > 0
		case character == 3 || character == 27:
			fmt.Print("\r\n")
			return "", false
		case character == 127 || character == 8:
			if len(input) > 0 {
				_, size := utf8.DecodeLastRune(input)
				input = input[:len(input)-size]
				fmt.Print("\b \b")
			}
		default:
			input = append(input, character)
			if character&0xC0 != 0x80 { // one dot per rune, not per UTF-8 byte
				fmt.Print("•")
			}
		}
	}
}

// run executes nmcli and reports the outcome; returns whether it succeeded.
func run(arguments ...string) bool {
	fmt.Printf("%sConnecting…%s", dim, reset)
	output, err := exec.Command("nmcli", arguments...).CombinedOutput()
	fmt.Print("\r\033[2K")
	if err != nil {
		fmt.Printf("%s✗ %s%s\n", red, strings.TrimSpace(strings.TrimPrefix(string(output), "Error: ")), reset)
		return false
	}
	return true
}

// shortcut: the password is passed as an nmcli argument, so it is briefly visible in the process list; upgrade to passwd-file if laptops are shared.
func connect(chosen network, known bool) {
	joinNew := func(arguments ...string) bool {
		if run(append([]string{"device", "wifi", "connect", chosen.ssid}, arguments...)...) {
			return true
		}
		// nmcli saves the profile even when activation fails; drop it so a bad attempt doesn't become "known".
		// Safe by name: the network wasn't known, so no profile with this name existed before.
		exec.Command("nmcli", "connection", "delete", "id", chosen.ssid).Run()
		return false
	}
	connected := (known && run("connection", "up", "id", chosen.ssid)) ||
		(!known && !chosen.secure && joinNew())
	for !connected && chosen.secure {
		password, ok := readPassword(chosen.ssid)
		if !ok {
			return
		}
		if known {
			connected = run("connection", "modify", "id", chosen.ssid, "wifi-sec.psk", password) && run("connection", "up", "id", chosen.ssid)
		} else {
			connected = joinNew("password", password)
		}
	}
	if connected {
		fmt.Printf("%s✓ Connected to %s%s%s\n", green, bold, chosen.ssid, reset)
	}
}

func newConnection() {
	fmt.Println(dim + "Scanning…" + reset)
	networks := parseNetworks(nmcli("-t", "-f", "SSID,SIGNAL,SECURITY", "device", "wifi", "list", "--rescan", "yes"))
	fmt.Print("\033[1A\033[2K")
	if len(networks) == 0 {
		fmt.Println(yellow + "No wifi networks found" + reset)
		return
	}
	known := knownSSIDs()
	width := 0
	for _, network := range networks {
		width = max(width, len(network.ssid))
	}
	label := func(network network) string {
		return fmt.Sprintf("%-*s  %s  %s", width, network.ssid, bars(network.signal), map[bool]string{true: yellow + "🔒" + reset, false: ""}[network.secure])
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
	connect(choices[choice], known[choices[choice].ssid])
}

func main() {
	fmt.Println(status())
	fmt.Println()
	if pick([]row{{label: "new connection"}, {label: "exit"}}) == 0 {
		newConnection()
	}
}
