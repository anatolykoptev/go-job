package websearch

import (
	"strings"
	"time"
)

const (
	trDay   = "day"
	trWeek  = "week"
	trMonth = "month"
	trYear  = "year"

	dateFormat       = "2006-01-02"
	yandexDateFormat = "20060102"
)

// timeRangeToBing maps SearXNG-style time_range values to Bing's freshness
// parameter. Bing supports Day/Week/Month and a custom YYYY-MM-DD..YYYY-MM-DD
// range; year is implemented as the last 365 days.
func timeRangeToBing(tr string) string {
	switch strings.ToLower(tr) {
	case trDay:
		return "Day"
	case trWeek:
		return "Week"
	case trMonth:
		return "Month"
	case trYear:
		now := time.Now()
		start := now.AddDate(0, 0, -365)
		return start.Format(dateFormat) + ".." + now.Format(dateFormat)
	default:
		return ""
	}
}

// timeRangeToBrave maps SearXNG-style time_range values to Brave HTML's tf
// parameter (pd/pw/pm/py).
func timeRangeToBrave(tr string) string {
	switch strings.ToLower(tr) {
	case trDay:
		return "pd"
	case trWeek:
		return "pw"
	case trMonth:
		return "pm"
	case trYear:
		return "py"
	default:
		return ""
	}
}

// timeRangeToDDG maps SearXNG-style time_range values to DuckDuckGo's df
// parameter (d/w/m/y).
func timeRangeToDDG(tr string) string {
	switch strings.ToLower(tr) {
	case trDay:
		return "d"
	case trWeek:
		return "w"
	case trMonth:
		return "m"
	case trYear:
		return "y"
	default:
		return ""
	}
}

// timeRangeToStartpage maps SearXNG-style time_range values to Startpage's
// with_date form field (d/w/m/y).
func timeRangeToStartpage(tr string) string {
	return timeRangeToDDG(tr)
}

// timeRangeToReddit maps SearXNG-style time_range values to Reddit's t
// parameter. Reddit uses the same lower-case names plus "all".
func timeRangeToReddit(tr string) string {
	switch strings.ToLower(tr) {
	case trDay, trWeek, trMonth, trYear:
		return strings.ToLower(tr)
	default:
		return ""
	}
}

// timeRangeToYepStart returns the start of the time range for Yep's
// start_crawl_date / start_published_date parameters. day/week/month/year
// are approximate offsets from now.
func timeRangeToYepStart(tr string) string {
	var d time.Duration
	switch strings.ToLower(tr) {
	case trDay:
		d = 24 * time.Hour
	case trWeek:
		d = 7 * 24 * time.Hour
	case trMonth:
		d = 30 * 24 * time.Hour
	case trYear:
		d = 365 * 24 * time.Hour
	default:
		return ""
	}
	return time.Now().UTC().Add(-d).Format(time.RFC3339)
}

// timeRangeToYandexDate maps SearXNG-style time_range values to Yandex's
// query-language date: operator. Yandex understands ranges in the form
// date:YYYYMMDD..YYYYMMDD; day/week/month/year are approximate offsets from
// now. The resulting string is appended to the query text.
func timeRangeToYandexDate(tr string) string {
	now := time.Now()
	var start time.Time
	switch strings.ToLower(tr) {
	case trDay:
		start = now.AddDate(0, 0, -1)
	case trWeek:
		start = now.AddDate(0, 0, -7)
	case trMonth:
		start = now.AddDate(0, 0, -30)
	case trYear:
		start = now.AddDate(0, 0, -365)
	default:
		return ""
	}
	return "date:" + start.Format(yandexDateFormat) + ".." + now.Format(yandexDateFormat)
}
