// Multi-port serial monitor for testing multi-device communication.
package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/jessevdk/go-flags"
	"tinygo.org/x/espflasher/pkg/monitor"
)

// Options contains command-line options.
type Options struct {
	Ports        string `short:"p" long:"ports" description:"Comma-separated list of serial ports (e.g., /dev/ttyUSB0,/dev/ttyUSB1 or A:/dev/ttyUSB0,B:/dev/ttyUSB1)"`
	BaudRate     int    `short:"b" long:"baud" description:"Baud rate" default:"115200"`
	ExitPattern  string `short:"e" long:"exit-on" description:"Exit when pattern is found in logs"`
	ExitTimeout  string `short:"t" long:"timeout" description:"Exit after duration (e.g., 30s, 5m)"`
	ExitPackets  int    `short:"n" long:"packets" description:"Exit after N packet-related logs"`
	Validate     bool   `short:"v" long:"validate" description:"Enable validation mode"`
	NoColor      bool   `short:"c" long:"no-color" description:"Disable colored output"`
	NoTimestamps bool   `short:"T" long:"no-timestamps" description:"Hide timestamps"`
	Reset        bool   `short:"r" long:"reset" description:"Reset devices before monitoring"`
	ShowVersion  bool   `short:"V" long:"version" description:"Show version and exit"`
}

const version = "0.1.0"

func main() {
	var opts Options
	parser := flags.NewParser(&opts, flags.Default)

	// Usage header
	parser.ShortDescription = "Multi-port serial monitor"
	parser.LongDescription = `Monitor multiple serial ports simultaneously with pattern matching
and validation support for multi-device communication testing.`

	// Parse arguments
	_, err := parser.Parse()
	if err != nil {
		if flagsErr, ok := err.(*flags.Error); ok && flagsErr.Type == flags.ErrHelp {
			os.Exit(0)
		}
		os.Exit(1)
	}

	if opts.ShowVersion {
		fmt.Printf("multi-monitor version %s\n", version)
		os.Exit(0)
	}

	// Validate arguments
	if len(opts.Ports) == 0 {
		fmt.Fprintln(os.Stderr, "Error: -ports is required")
		fmt.Fprintln(os.Stderr, "Usage: multi-monitor -ports /dev/ttyUSB0,/dev/ttyUSB1")
		fmt.Fprintln(os.Stderr, "       multi-monitor -p HOST:/dev/ttyUSB0,CLIENT:/dev/ttyUSB1")
		parser.WriteHelp(os.Stderr)
		os.Exit(1)
	}

	// Parse port configurations
	portConfigs, err := parsePortConfigs(opts.Ports)
	if err != nil {
		log.Fatalf("Error parsing ports: %v", err)
	}

	if len(portConfigs) < 1 {
		log.Fatal("Error: at least one port must be specified")
	}

	// Parse timeout if specified
	var timeout time.Duration
	if opts.ExitTimeout != "" {
		timeout, err = time.ParseDuration(opts.ExitTimeout)
		if err != nil {
			log.Fatalf("Error parsing timeout: %v", err)
		}
	}

	// Build monitor config
	cfg := monitor.MonitorConfig{
		Ports:          portConfigs,
		BaudRate:       opts.BaudRate,
		ValidateMode:   opts.Validate,
		NoColor:        opts.NoColor,
		HideTimestamps: opts.NoTimestamps,
	}

	if opts.ExitPattern != "" || timeout > 0 || opts.ExitPackets > 0 {
		cfg.ExitCondition = &monitor.ExitCondition{
			Pattern: opts.ExitPattern,
			Timeout: timeout,
			Packets: opts.ExitPackets,
		}
	}

	// Create validator if requested
	var validator *monitor.NetworkValidator
	if opts.Validate {
		// Default expected packets if not specified
		expectedPackets := opts.ExitPackets
		if expectedPackets == 0 {
			expectedPackets = 100 // Default for validation mode
		}
		validator = monitor.NewNetworkValidator(expectedPackets)
		cfg.Validator = validator
		log.Printf("Validation mode: expecting %d packets", expectedPackets)
	}

	// Create and run monitor
	m := monitor.NewMultiMonitor(cfg)

	if err := m.Open(); err != nil {
		log.Fatalf("Error opening ports: %v", err)
	}
	defer m.Close()

	// Run monitor (blocking) - reset before capture if requested
	if err := m.Run(opts.Reset); err != nil {
		log.Fatalf("Monitor error: %v", err)
	}

	// Print validation report
	if validator != nil {
		fmt.Print(validator.Report())
	}
}

// parsePortConfigs parses the ports argument into PortConfig list.
// Formats:
//   - /dev/ttyUSB0,/dev/ttyUSB1 (uses default labels A, B)
//   - A:/dev/ttyUSB0,B:/dev/ttyUSB1 (uses custom labels)
func parsePortConfigs(portsArg string) ([]monitor.PortConfig, error) {
	var configs []monitor.PortConfig
	defaultLabels := []string{"A", "B", "C", "D"}

	parts := strings.Split(portsArg, ",")
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		var label, name string

		// Check for "LABEL:port" format
		if colonIdx := strings.Index(part, ":"); colonIdx > 0 {
			label = strings.TrimSpace(part[:colonIdx])
			name = strings.TrimSpace(part[colonIdx+1:])
		} else {
			// Use default label
			if i < len(defaultLabels) {
				label = defaultLabels[i]
			} else {
				label = fmt.Sprintf("%d", i)
			}
			name = part
		}

		// Validate path
		if !strings.HasPrefix(name, "/") {
			return nil, fmt.Errorf("port path must be absolute: %s", name)
		}

		// Assign color based on index
		color := monitor.ColorGray
		switch i % 6 {
		case 0:
			color = monitor.ColorCyan
		case 1:
			color = monitor.ColorYellow
		case 2:
			color = monitor.ColorGreen
		case 3:
			color = monitor.ColorMagenta
		case 4:
			color = monitor.ColorBlue
		case 5:
			color = monitor.ColorRed
		}

		configs = append(configs, monitor.PortConfig{
			Name:   name,
			Label:  label,
			Prefix: "[" + label + "] ",
			Color:  color,
		})
	}

	return configs, nil
}
