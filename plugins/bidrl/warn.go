package bidrl

import (
	"database/sql"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

// A saved lot's warning schedule is configured as durations such as "24h" or
// "10m". The alert table records the duration in seconds, rather than putting a
// fixed number of stages on bidrl_lots, so an operator can add or remove stages
// without another migration.
type warnStage struct {
	lead   time.Duration
	title  string
	phrase string
}

var defaultWarnLeadTimes = []time.Duration{
	24 * time.Hour,
	4 * time.Hour,
	1 * time.Hour,
	10 * time.Minute,
}

type warnCandidate struct {
	id, title, ends string
	currentBidCents *int64
	bidCount        *int
	// alerted is keyed by the configured lead time in seconds.
	alerted map[int64]bool
}

func (p *Plugin) warnStages() []warnStage {
	c := p.cfg()
	leads := append([]time.Duration(nil), defaultWarnLeadTimes...)
	if c.SavedAlertLeadTimes != nil {
		leads = make([]time.Duration, 0, len(c.SavedAlertLeadTimes))
		seen := make(map[time.Duration]struct{}, len(c.SavedAlertLeadTimes))
		for _, raw := range c.SavedAlertLeadTimes {
			d, err := time.ParseDuration(strings.TrimSpace(raw))
			if err != nil || d <= 0 {
				continue
			}
			if _, ok := seen[d]; ok {
				continue
			}
			seen[d] = struct{}{}
			leads = append(leads, d)
		}
	}
	// The warning job chooses the tightest window that has been entered. Sorting
	// ascending also makes a newly saved lot inside several windows fire only its
	// closest one; the wider windows are marked as overtaken below.
	sort.Slice(leads, func(i, j int) bool { return leads[i] < leads[j] })
	stages := make([]warnStage, 0, len(leads))
	for _, lead := range leads {
		window := humanWindow(lead)
		stages = append(stages, warnStage{
			lead:   lead,
			title:  "BIDRL saved lot closing in " + window,
			phrase: "closes within %s",
		})
	}
	return stages
}

func (s warnStage) leadSeconds() int64 { return int64(s.lead / time.Second) }

func (p *Plugin) warnJob(jc hostjobs.Context) error {
	h, ok := p.host()
	if !ok {
		return fmt.Errorf("bidrl: host is not initialized")
	}
	stages := p.warnStages()
	if len(stages) == 0 {
		return nil
	}
	now := h.Clock().Now()

	// The join returns one row per recorded stage. Building candidates here keeps
	// the query bounded to saved lots while allowing the configured stage count to
	// change without dynamic SQL or a read per stage.
	rows, err := h.Store().Query(jc, `
		SELECT l.id, l.title, l.ends_at, l.current_bid_cents, l.bid_count, COALESCE(a.lead_seconds, 0)
		  FROM bidrl_lots l
		  LEFT JOIN bidrl_lot_alerts a ON a.lot_id = l.id
		 WHERE l.ends_at != ''
		   AND EXISTS (SELECT 1 FROM bidrl_favorites f WHERE f.lot_id = l.id)
		 ORDER BY l.ends_at, l.id`)
	if err != nil {
		return err
	}
	var lots []*warnCandidate
	byID := make(map[string]*warnCandidate)
	for rows.Next() {
		var id, title, ends string
		var currentBid, bidCount sql.NullInt64
		var leadSeconds int64
		if err := rows.Scan(&id, &title, &ends, &currentBid, &bidCount, &leadSeconds); err != nil {
			rows.Close()
			return err
		}
		c := byID[id]
		if c == nil {
			var currentBidCents *int64
			if currentBid.Valid {
				v := currentBid.Int64
				currentBidCents = &v
			}
			var bids *int
			if bidCount.Valid {
				v := int(bidCount.Int64)
				bids = &v
			}
			c = &warnCandidate{
				id: id, title: title, ends: ends,
				currentBidCents: currentBidCents, bidCount: bids,
				alerted: map[int64]bool{},
			}
			byID[id] = c
			lots = append(lots, c)
		}
		if leadSeconds > 0 {
			c.alerted[leadSeconds] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}

	marked := now.UTC().Format(time.RFC3339Nano)
	for _, c := range lots {
		stage, ok := dueStage(*c, stages, now)
		if !ok {
			continue
		}
		s := stages[stage]
		body := c.title + " — " + warningBidDetails(c.currentBidCents, c.bidCount) + "; " + fmt.Sprintf(s.phrase, humanWindow(s.lead))
		if err := h.Store().Tx(jc, func(tx hoststorage.Tx) error {
			// Insert the selected stage first. Its unique key is the durable
			// exactly-once guard for the event publication.
			result, err := tx.Exec(jc, `
				INSERT OR IGNORE INTO bidrl_lot_alerts(lot_id, lead_seconds, alerted_at)
				VALUES (?, ?, ?)`, c.id, s.leadSeconds(), marked)
			if err != nil {
				return err
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if affected == 0 {
				// Another worker won the same stage between the candidate query and
				// this transaction. Do not publish a duplicate alert.
				return nil
			}
			// If the lot was first observed inside a narrow window, the wider
			// windows have been overtaken and must never fire later.
			for i := stage + 1; i < len(stages); i++ {
				if _, err := tx.Exec(jc, `
					INSERT OR IGNORE INTO bidrl_lot_alerts(lot_id, lead_seconds, alerted_at)
					VALUES (?, ?, ?)`, c.id, stages[i].leadSeconds(), marked); err != nil {
					return err
				}
			}
			return h.Events().PublishTx(jc, tx, "alert", body, alerted{
				Title: s.title, Body: body, LotID: c.id, EndsAt: c.ends,
				CurrentBidCents: c.currentBidCents, BidCount: c.bidCount,
				URL: "/bidrl/lot/" + url.PathEscape(c.id),
			})
		}); err != nil {
			return err
		}
	}
	return nil
}

// dueStage picks the tightest stage whose window this lot has entered and which
// has not already fired for it.
func dueStage(c warnCandidate, stages []warnStage, now time.Time) (int, bool) {
	for i, s := range stages {
		if c.alerted[s.leadSeconds()] {
			continue
		}
		if endingWithin(c.ends, now, s.lead) {
			return i, true
		}
	}
	return 0, false
}

func warningBidDetails(currentBidCents *int64, bidCount *int) string {
	price := "unknown"
	if currentBidCents != nil {
		price = bidAmount(*currentBidCents)
	}
	bids := "bid count unknown"
	if bidCount != nil {
		bids = fmt.Sprintf("%d bid%s", *bidCount, plural(*bidCount))
	}
	return "current bid " + price + ", " + bids
}

func bidAmount(cents int64) string {
	if cents < 0 {
		return "-$" + fmt.Sprintf("%d.%02d", (-cents)/100, (-cents)%100)
	}
	return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
}

// humanWindow says a warning window the way the alert should read it.
func humanWindow(d time.Duration) string {
	if d >= time.Hour && d%time.Hour == 0 {
		hours := int(d / time.Hour)
		return fmt.Sprintf("%d hour%s", hours, plural(hours))
	}
	if d >= time.Minute && d%time.Minute == 0 {
		minutes := int(d / time.Minute)
		return fmt.Sprintf("%d minute%s", minutes, plural(minutes))
	}
	return d.String()
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
