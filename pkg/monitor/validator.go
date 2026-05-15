// Package monitor provides serial port monitoring with pattern parsing.
package monitor

import (
	"fmt"
	"sync"
	"time"
)

// NetworkValidator tracks network communication between two devices.
type NetworkValidator struct {
	mu sync.Mutex

	// Tracking
	expectedPackets int
	seenPackets     int

	// Latency tracking
	sendTimestamps map[string]time.Time // key: "playerX-ticN"
	latencies      []time.Duration

	// Packet flow tracking
	player0Sends int
	player0Recvs int
	player1Sends int
	player1Recvs int

	// Correlation errors
	mismatchedPackets int
	missingPackets    int

	// Start time
	startTime time.Time
}

// ValidatorConfig holds configuration for network validation.
type ValidatorConfig struct {
	ExpectedPackets int // Expected number of packets to validate
	Timeout         time.Duration
}

// NewNetworkValidator creates a new network validator.
func NewNetworkValidator(expectedPackets int) *NetworkValidator {
	return &NetworkValidator{
		expectedPackets: expectedPackets,
		sendTimestamps:  make(map[string]time.Time),
		latencies:       make([]time.Duration, 0, expectedPackets),
		startTime:       time.Now(),
	}
}

// ProcessEntry processes a log entry and updates validation state.
func (v *NetworkValidator) ProcessEntry(entry LogEntry) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if entry.PacketSend {
		v.seenPackets++
		if entry.PlayerTo == 0 {
			v.player1Sends++
		} else if entry.PlayerTo == 1 {
			v.player0Sends++
		}

		// Track send timestamp for latency calculation
		if entry.TicNum >= 0 {
			key := fmt.Sprintf("player%d-tic%d", entry.PlayerTo, entry.TicNum)
			v.sendTimestamps[key] = entry.Timestamp
		}
	}

	if entry.PacketRecv {
		v.seenPackets++
		if entry.PlayerFrom == 0 {
			v.player1Recvs++
		} else if entry.PlayerFrom == 1 {
			v.player0Recvs++
		}

		// Calculate latency if we have a send timestamp
		if entry.TicNum >= 0 {
			key := fmt.Sprintf("player%d-tic%d", entry.PlayerFrom, entry.TicNum)
			if sendTime, ok := v.sendTimestamps[key]; ok {
				latency := entry.Timestamp.Sub(sendTime)
				if latency > 0 && latency < 5*time.Second {
					v.latencies = append(v.latencies, latency)
				}
				delete(v.sendTimestamps, key)
			} else {
				v.missingPackets++
			}
		}
	}
}

// IsComplete returns true if validation is complete.
func (v *NetworkValidator) IsComplete() bool {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.expectedPackets > 0 {
		return v.seenPackets >= v.expectedPackets
	}
	return false
}

// Report returns a validation report.
func (v *NetworkValidator) Report() string {
	v.mu.Lock()
	defer v.mu.Unlock()

	elapsed := time.Since(v.startTime)

	report := "\n=== Network Validation Report ===\n"
	report += fmt.Sprintf("Duration: %s\n", elapsed.Round(time.Millisecond))
	report += fmt.Sprintf("Packets seen: %d", v.seenPackets)

	if v.expectedPackets > 0 {
		successRate := float64(v.seenPackets) / float64(v.expectedPackets) * 100
		report += fmt.Sprintf(" / %d (%.1f%%)", v.expectedPackets, successRate)
	}
	report += "\n"

	// Packet flow
	report += "\nPacket Flow:\n"
	report += fmt.Sprintf("  Player 0: %d sent, %d received\n", v.player0Sends, v.player0Recvs)
	report += fmt.Sprintf("  Player 1: %d sent, %d received\n", v.player1Sends, v.player1Recvs)

	// Latency statistics
	if len(v.latencies) > 0 {
		var sum time.Duration
		min := v.latencies[0]
		max := v.latencies[0]

		for _, l := range v.latencies {
			sum += l
			if l < min {
				min = l
			}
			if l > max {
				max = l
			}
		}
		avg := sum / time.Duration(len(v.latencies))

		report += fmt.Sprintf("\nLatency (n=%d):\n", len(v.latencies))
		report += fmt.Sprintf("  Min: %s\n", min.Round(time.Microsecond))
		report += fmt.Sprintf("  Avg: %s\n", avg.Round(time.Microsecond))
		report += fmt.Sprintf("  Max: %s\n", max.Round(time.Microsecond))
	}

	// Issues
	if v.missingPackets > 0 {
		report += fmt.Sprintf("\n⚠ Missing send timestamps: %d\n", v.missingPackets)
	}
	if v.mismatchedPackets > 0 {
		report += fmt.Sprintf("⚠ Mismatched packets: %d\n", v.mismatchedPackets)
	}

	// Verdict
	report += "\nVerdict: "
	if v.expectedPackets > 0 && v.seenPackets >= v.expectedPackets {
		report += "✓ PASS - Expected packets received\n"
	} else if len(v.latencies) > 0 {
		avgLatency := v.latencies[len(v.latencies)/2] // median-ish
		if avgLatency < 50*time.Millisecond {
			report += "✓ GOOD - Low latency communication\n"
		} else if avgLatency < 200*time.Millisecond {
			report += "⚠ OK - Moderate latency\n"
		} else {
			report += "✗ POOR - High latency detected\n"
		}
	} else {
		report += "? UNKNOWN - Insufficient data\n"
	}

	return report
}

// GetLatencyStats returns current latency statistics.
func (v *NetworkValidator) GetLatencyStats() (min, avg, max time.Duration, count int) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if len(v.latencies) == 0 {
		return 0, 0, 0, 0
	}

	var sum time.Duration
	min = v.latencies[0]
	max = v.latencies[0]

	for _, l := range v.latencies {
		sum += l
		if l < min {
			min = l
		}
		if l > max {
			max = l
		}
	}

	return min, sum / time.Duration(len(v.latencies)), max, len(v.latencies)
}

// GetPacketCounts returns packet send/receive counts.
func (v *NetworkValidator) GetPacketCounts() (p0Send, p0Recv, p1Send, p1Recv int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.player0Sends, v.player0Recvs, v.player1Sends, v.player1Recvs
}
