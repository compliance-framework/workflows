package manifest

import "time"

// NextWorkingWeekday returns date itself if it is a working weekday, otherwise
// the first later day that is: not a Saturday or Sunday, and not one of the
// manifest's holidays. Days are compared in date's location, and the
// time of day is kept.
//
// The train uses it to move a scheduled day that falls on a weekend or holiday,
// e.g. 2027-01-01 (a Friday and a holiday) becomes 2027-01-04.
func (m *Manifest) NextWorkingWeekday(date time.Time) time.Time {
	holidays := make(map[string]bool, len(m.Holidays))
	for _, h := range m.Holidays {
		holidays[h] = true
	}
	for {
		wd := date.Weekday()
		if wd != time.Saturday && wd != time.Sunday && !holidays[date.Format(dateLayout)] {
			return date
		}
		date = date.AddDate(0, 0, 1)
	}
}
