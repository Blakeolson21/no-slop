package lanehealth

import (
	"fmt"
	"time"
)

func resetTimezone(t time.Time) string {
	zone := t.Location().String()
	if zone == "Local" {
		// "Local" would mean a different zone if another process reads this file.
		zone, _ = t.Zone()
	}
	return zone
}

// FormatResetTime keeps the interpreted provider clock and its explicit offset
// visible even when the reader runs in a different timezone. Legacy marks have
// no source-zone evidence and retain their existing local-time presentation.
func FormatResetTime(until time.Time, zone string) string {
	if zone == "" {
		return until.Local().Format("2006-01-02 15:04 MST")
	}
	// The stored offset is authoritative. Use a named location only if its
	// current tzdata agrees at this instant; otherwise retain the saved offset.
	_, offset := until.Zone()
	loc := time.FixedZone(zone, offset)
	if named, err := time.LoadLocation(zone); err == nil {
		if _, namedOffset := until.In(named).Zone(); namedOffset == offset {
			loc = named
		}
	}
	return fmt.Sprintf("%s (%s)", until.In(loc).Format("2006-01-02 15:04 MST -07:00"), zone)
}

// ResetTime renders the persisted reset instant without losing its source zone.
func (o Outage) ResetTime() string { return FormatResetTime(o.Until, o.ResetTimezone) }
