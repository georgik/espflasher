// Package monitor provides serial port monitoring with pattern parsing.
package monitor

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"go.bug.st/serial"
	"golang.org/x/term"
)

// PortConfig holds configuration for a single serial port.
type PortConfig struct {
	Name   string // Serial port path (e.g., /dev/ttyUSB0)
	Label  string // Display label (e.g., "HOST")
	Prefix string // Output prefix (e.g., "[A] ")
	Color  string // ANSI color code
}

// Default colors for ports.
var (
	ColorReset   = "\033[0m"
	ColorRed     = "\033[31m"
	ColorGreen   = "\033[32m"
	ColorYellow  = "\033[33m"
	ColorBlue    = "\033[34m"
	ColorMagenta = "\033[35m"
	ColorCyan    = "\033[36m"
	ColorWhite   = "\033[37m"
	ColorGray    = "\033[90m"
)

// DefaultPortConfigs returns default port configurations for dual monitoring.
func DefaultPortConfigs() []PortConfig {
	return []PortConfig{
		{Label: "A", Prefix: "[A] ", Color: ColorCyan},
		{Label: "B", Prefix: "[B] ", Color: ColorYellow},
	}
}

// MonitorConfig holds configuration for the multi-port monitor.
type MonitorConfig struct {
	Ports          []PortConfig
	BaudRate       int
	ExitCondition  *ExitCondition
	ValidateMode   bool
	NoColor        bool
	HideTimestamps bool
	Validator      *NetworkValidator // Optional validator for network tracking
}

// MultiMonitor monitors multiple serial ports concurrently.
type MultiMonitor struct {
	config    MonitorConfig
	ports     []serialPort
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	mu        sync.Mutex
	stats     map[string]*PortStats
	entryChan chan LogEntry
	done      chan struct{}
}

type serialPort struct {
	port serial.Port
	info PortConfig
}

// PortStats tracks statistics for a single port.
type PortStats struct {
	LinesReceived int
	PacketsSent   int
	PacketsRecv   int
	Errors        int
	FirstSeen     time.Time
	LastSeen      time.Time
}

// NewMultiMonitor creates a new multi-port monitor.
func NewMultiMonitor(config MonitorConfig) *MultiMonitor {
	ctx, cancel := context.WithCancel(context.Background())
	if len(config.Ports) == 0 {
		config.Ports = DefaultPortConfigs()
	}
	return &MultiMonitor{
		config:    config,
		ctx:       ctx,
		cancel:    cancel,
		stats:     make(map[string]*PortStats),
		entryChan: make(chan LogEntry, 100),
		done:      make(chan struct{}),
	}
}

// Open opens all configured serial ports.
func (m *MultiMonitor) Open() error {
	for i, pc := range m.config.Ports {
		mode := &serial.Mode{
			BaudRate: m.config.BaudRate,
		}

		port, err := serial.Open(pc.Name, mode)
		if err != nil {
			// Close any already opened ports
			m.Close()
			return fmt.Errorf("open port %s: %w", pc.Name, err)
		}

		// Set read timeout for non-blocking reads
		if err := port.SetReadTimeout(50 * time.Millisecond); err != nil {
			port.Close()
			m.Close()
			return fmt.Errorf("set read timeout for %s: %w", pc.Name, err)
		}

		// Initialize stats
		m.mu.Lock()
		if m.config.Ports[i].Label == "" {
			m.config.Ports[i].Label = fmt.Sprintf("%d", i)
		}
		if m.config.Ports[i].Prefix == "" {
			m.config.Ports[i].Prefix = fmt.Sprintf("[%s] ", m.config.Ports[i].Label)
		}
		m.stats[pc.Name] = &PortStats{
			FirstSeen: time.Now(),
		}
		m.mu.Unlock()

		m.ports = append(m.ports, serialPort{port: port, info: m.config.Ports[i]})
	}
	return nil
}

// Close closes all serial ports.
func (m *MultiMonitor) Close() error {
	m.cancel()
	m.wg.Wait()

	var firstErr error
	for _, sp := range m.ports {
		if err := sp.port.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	m.ports = nil
	return firstErr
}

// printToOutput prints a message directly to stdout (bypasses entry channel).
func (m *MultiMonitor) printToOutput(msg string) {
	color := ""
	reset := ""
	if !m.config.NoColor {
		reset = ColorReset
	}
	fmt.Printf("%s%s%s\n", color, msg, reset)
}

// Run starts the monitor. If resetFirst is true, resets devices before monitoring.
func (m *MultiMonitor) Run(resetFirst bool) error {
	if len(m.ports) == 0 {
		return fmt.Errorf("no ports opened")
	}

	// Start reader goroutines FIRST (before reset to capture boot logs)
	for _, sp := range m.ports {
		m.wg.Add(1)
		go m.readPort(sp)
	}

	// Start output handler
	m.wg.Add(1)
	go m.handleOutput()

	// Reset devices AFTER readers are running (to capture boot logs)
	if resetFirst {
		for _, sp := range m.ports {
			label := sp.info.Label
			if label == "" {
				label = sp.info.Name
			}
			m.printToOutput(fmt.Sprintf("Resetting %s (%s)...", label, sp.info.Name))

			// ESP32 classic reset (from esptool.py)
			// RTS controls EN (reset), DTR controls IO0 (boot mode)
			// For normal boot: IO0=HIGH (internal pullup), toggle EN

			sp.port.SetDTR(false) // IO0 HIGH (normal boot)
			sp.port.SetRTS(true)  // EN LOW - reset
			time.Sleep(100 * time.Millisecond)

			sp.port.SetRTS(false) // EN HIGH - release reset
			time.Sleep(50 * time.Millisecond)

			sp.port.SetDTR(false) // Ensure IO0 HIGH
		}

		// Give devices time to boot while readers are capturing
		time.Sleep(2 * time.Second)
	}

	// Set up signal handler
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Set terminal to raw mode for CTRL+C detection
	stdinFd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(stdinFd)
	if err != nil {
		// Non-fatal, continue without raw mode
		fmt.Fprintf(os.Stderr, "Warning: could not set raw mode: %v\n", err)
	} else {
		defer term.Restore(stdinFd, oldState)
	}

	// Print header
	m.printHeader()

	startTime := time.Now()
	totalPacketCount := 0

	// Main loop
	for {
		select {
		case <-m.ctx.Done():
			return nil

		case <-sigCh:
			fmt.Println("\r\nExiting monitor...")
			return nil

		case entry := <-m.entryChan:
			// Update stats
			m.mu.Lock()
			if stats, ok := m.stats[entry.Source]; ok {
				stats.LinesReceived++
				stats.LastSeen = time.Now()
				if entry.PacketSend {
					stats.PacketsSent++
				}
				if entry.PacketRecv {
					stats.PacketsRecv++
				}
				if entry.IsError() {
					stats.Errors++
				}
			}
			m.mu.Unlock()

			// Feed validator if configured
			if m.config.Validator != nil {
				m.config.Validator.ProcessEntry(entry)
			}

			// Check exit condition
			if entry.IsPacketRelated() {
				totalPacketCount++
			}
			if m.config.ExitCondition != nil {
				elapsed := time.Since(startTime)
				if m.config.ExitCondition.ShouldExit(entry, elapsed, totalPacketCount) {
					m.printSummary(startTime, totalPacketCount)
					return nil
				}
			}

		case <-time.After(100 * time.Millisecond):
			// Check for timeout exit condition
			if m.config.ExitCondition != nil && m.config.ExitCondition.Timeout > 0 {
				elapsed := time.Since(startTime)
				if elapsed >= m.config.ExitCondition.Timeout {
					m.printSummary(startTime, totalPacketCount)
					return nil
				}
			}
		}
	}
}

// readPort reads from a single serial port and sends entries to the channel.
func (m *MultiMonitor) readPort(sp serialPort) {
	defer m.wg.Done()

	buf := make([]byte, 1024)
	lineBuf := make([]byte, 0, 1024)

	for {
		select {
		case <-m.ctx.Done():
			return
		default:
		}

		n, err := sp.port.Read(buf)
		if err != nil && err != io.EOF {
			// Timeout is expected
			continue
		}

		if n > 0 {
			// Process buffer line by line
			for _, b := range buf[:n] {
				if b == '\n' {
					line := string(lineBuf)
					if len(line) > 0 {
						entry := ParseLogEntry(sp.info.Name, line)
						entry.Source = sp.info.Name
						m.entryChan <- entry
					}
					lineBuf = lineBuf[:0]
				} else if b != '\r' {
					lineBuf = append(lineBuf, b)
				}
			}
		}
	}
}

// handleOutput processes log entries and prints them.
func (m *MultiMonitor) handleOutput() {
	defer m.wg.Done()

	for {
		select {
		case <-m.ctx.Done():
			return
		case entry := <-m.entryChan:
			m.printEntry(entry)
		}
	}
}

// printEntry prints a log entry to stdout.
func (m *MultiMonitor) printEntry(entry LogEntry) {
	prefix := ""
	color := ""
	reset := ""

	if !m.config.NoColor {
		// Find port color
		for _, pc := range m.config.Ports {
			if pc.Name == entry.Source {
				color = pc.Color
				break
			}
		}
		reset = ColorReset
	}

	// Find port prefix
	for _, pc := range m.config.Ports {
		if pc.Name == entry.Source {
			prefix = pc.Prefix
			break
		}
	}

	if m.config.ValidateMode && !entry.IsPacketRelated() && !entry.IsError() {
		// In validate mode, only show packet-related and error messages
		return
	}

	timestamp := ""
	if !m.config.HideTimestamps {
		timestamp = entry.Timestamp.Format("15:04:05.000 ")
	}

	fmt.Printf("%s%s%s%s%s\n", color, timestamp, prefix, reset, entry.Raw)
}

// printHeader prints the monitor header.
func (m *MultiMonitor) printHeader() {
	fmt.Println("Multi-Port Serial Monitor")
	fmt.Println("-------------------------")
	for i, sp := range m.ports {
		fmt.Printf("  Port %d: %s @ %d baud (%s)\n",
			i, sp.info.Name, m.config.BaudRate, sp.info.Label)
	}
	if m.config.ExitCondition != nil {
		if m.config.ExitCondition.Pattern != "" {
			fmt.Printf("  Exit on pattern: %s\n", m.config.ExitCondition.Pattern)
		}
		if m.config.ExitCondition.Timeout > 0 {
			fmt.Printf("  Timeout: %s\n", m.config.ExitCondition.Timeout)
		}
		if m.config.ExitCondition.Packets > 0 {
			fmt.Printf("  Exit after %d packets\n", m.config.ExitCondition.Packets)
		}
	}
	fmt.Println("CTRL+C to exit")
	fmt.Println("---")
}

// printSummary prints the monitoring summary.
func (m *MultiMonitor) printSummary(startTime time.Time, totalPackets int) {
	elapsed := time.Since(startTime)
	fmt.Println("\r\n--- Summary ---")
	fmt.Printf("Duration: %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("Total packet-related logs: %d\n", totalPackets)

	m.mu.Lock()
	for name, stats := range m.stats {
		label := name
		for _, pc := range m.config.Ports {
			if pc.Name == name {
				label = pc.Label
				break
			}
		}
		fmt.Printf("  %s: %d lines, %d sent, %d recv, %d errors\n",
			label, stats.LinesReceived, stats.PacketsSent, stats.PacketsRecv, stats.Errors)
	}
	m.mu.Unlock()
}

// GetStats returns a copy of the current statistics.
func (m *MultiMonitor) GetStats() map[string]PortStats {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make(map[string]PortStats, len(m.stats))
	for k, v := range m.stats {
		result[k] = *v
	}
	return result
}
