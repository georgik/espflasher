// Package monitor provides serial port monitoring with pattern parsing.
package monitor

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// PacketType represents a Doom network packet type.
type PacketType int

const (
	PacketUnknown PacketType = iota
	PacketInit
	PacketSetup
	PacketGo
	PacketTics
	PacketTicc
	PacketQuit
	PacketRetrans
	PacketDown
	PacketExtra
)

// PacketTypeFromString converts packet type string/number to PacketType.
func PacketTypeFromString(s string) PacketType {
	switch s {
	case "PKT_INIT", "INIT", "0":
		return PacketInit
	case "PKT_SETUP", "SETUP", "1":
		return PacketSetup
	case "PKT_GO", "GO", "2":
		return PacketGo
	case "PKT_TICS", "TICS", "3":
		return PacketTics
	case "PKT_TICC", "TICC", "4":
		return PacketTicc
	case "PKT_QUIT", "QUIT", "5":
		return PacketQuit
	case "PKT_RETRANS", "RETRANS", "6":
		return PacketRetrans
	case "PKT_DOWN", "DOWN", "7":
		return PacketDown
	case "PKT_EXTRA", "EXTRA", "8":
		return PacketExtra
	default:
		// Try to parse as number
		if n, err := strconv.Atoi(s); err == nil {
			return PacketType(n)
		}
		return PacketUnknown
	}
}

// String returns the packet type name.
func (p PacketType) String() string {
	switch p {
	case PacketInit:
		return "INIT"
	case PacketSetup:
		return "SETUP"
	case PacketGo:
		return "GO"
	case PacketTics:
		return "TICS"
	case PacketTicc:
		return "TICC"
	case PacketQuit:
		return "QUIT"
	case PacketRetrans:
		return "RETRANS"
	case PacketDown:
		return "DOWN"
	case PacketExtra:
		return "EXTRA"
	default:
		return "UNKNOWN"
	}
}

// LogEntry represents a parsed Doom log entry.
type LogEntry struct {
	Timestamp time.Time
	Source    string // Port name
	Raw       string
	Level     string // I, W, E, D
	Tag       string // Log tag
	Message   string

	// Parsed network fields
	PacketSend bool
	PacketRecv bool
	PacketType PacketType
	PacketLen  int
	PlayerFrom int
	PlayerTo   int
	TicNum     int
}

var (
	// ESP-IDF log pattern with optional timestamp prefix:
	// HH:MM:SS.mmm [Tag] I (123) tag: message
	// or: I (123) tag: message
	espIDFLog = regexp.MustCompile(`(?:\d{2}:\d{2}:\d{2}\.\d{3} \[.\] )?([IDEW]) \(\d+\) (\w+): (.+)$`)
	// Doom ESP-NOW send: I_GetPacket: got X bytes, type=Y, from player Z
	recvPacket = regexp.MustCompile(`I_GetPacket: got (\d+) bytes, type=(\d+), from player (\d+)`)
	// Send packet: Sending PKT_TICC: sendtics=Z player=Y
	sendPacket = regexp.MustCompile(`Sending (PKT_\w+):`)
	// ESP-NOW send: doom_espnow_send_to_player: sending to player (\d+)
	sendToPlayer = regexp.MustCompile(`sending to player (\d+)`)
	// Player info: Player X: XX:XX:XX:XX:XX:XX
	playerInfo = regexp.MustCompile(`Player (\d+): ([0-9a-fA-F:]{17})`)
	// Tic packet with tic number: packet_set.*tic=(\d+)
	packetTic = regexp.MustCompile(`tic[=:](\d+)`)
)

// ParseLogEntry parses a line of Doom log output.
func ParseLogEntry(source, line string) LogEntry {
	entry := LogEntry{
		Timestamp: time.Now(),
		Source:    source,
		Raw:       line,
	}

	// Try ESP-IDF log format first
	if matches := espIDFLog.FindStringSubmatch(line); len(matches) == 4 {
		entry.Level = matches[1]
		entry.Tag = matches[2]
		entry.Message = matches[3]

		// Parse packet receive
		if matches := recvPacket.FindStringSubmatch(entry.Message); len(matches) == 4 {
			entry.PacketRecv = true
			entry.PacketLen, _ = strconv.Atoi(matches[1])
			entry.PacketType = PacketTypeFromString(matches[2])
			entry.PlayerFrom, _ = strconv.Atoi(matches[3])
		}

		// Parse packet send
		if matches := sendPacket.FindStringSubmatch(entry.Message); len(matches) == 2 {
			entry.PacketSend = true
			entry.PacketType = PacketTypeFromString(matches[1])
		}

		// Parse send to player
		if matches := sendToPlayer.FindStringSubmatch(entry.Message); len(matches) == 2 {
			entry.PlayerTo, _ = strconv.Atoi(matches[1])
		}

		// Parse tic number
		if matches := packetTic.FindStringSubmatch(entry.Message); len(matches) == 2 {
			entry.TicNum, _ = strconv.Atoi(matches[1])
		}

		return entry
	}

	// Fallback: treat entire line as message
	entry.Message = line

	// Still try to match packet patterns
	if matches := recvPacket.FindStringSubmatch(line); len(matches) == 4 {
		entry.PacketRecv = true
		entry.PacketLen, _ = strconv.Atoi(matches[1])
		entry.PacketType = PacketTypeFromString(matches[2])
		entry.PlayerFrom, _ = strconv.Atoi(matches[3])
	}

	return entry
}

// IsPacketRelated returns true if the log entry is related to packet communication.
func (e LogEntry) IsPacketRelated() bool {
	return e.PacketSend || e.PacketRecv
}

// IsError returns true if the log entry is an error or warning.
func (e LogEntry) IsError() bool {
	return e.Level == "E" || e.Level == "W"
}

// ContainsPattern returns true if the log entry contains the given pattern.
func (e LogEntry) ContainsPattern(pattern string) bool {
	if pattern == "" {
		return false
	}
	return strings.Contains(e.Raw, pattern) ||
		strings.Contains(e.Message, pattern)
}

// ExitCondition represents a condition for exiting the monitor.
type ExitCondition struct {
	Pattern string
	Timeout time.Duration
	Packets int // Exit after receiving N packet-related logs
}

// ShouldExit returns true if the exit condition is met.
func (e *ExitCondition) ShouldExit(entry LogEntry, elapsed time.Duration, packetCount int) bool {
	if e.Pattern != "" && entry.ContainsPattern(e.Pattern) {
		return true
	}
	if e.Timeout > 0 && elapsed >= e.Timeout {
		return true
	}
	if e.Packets > 0 && packetCount >= e.Packets {
		return true
	}
	return false
}
