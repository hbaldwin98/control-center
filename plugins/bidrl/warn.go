package bidrl

import (
	"fmt"
	"strings"
	"time"

	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

// A saved lot is warned about twice: once the day before it closes, and once
// shortly before it actually does. Each warning has its own column, so one firing
// never suppresses the other and neither can fire twice.
type warnStage struct {
	// column is the bidrl_lots column that records this stage having fired. It is a
	// constant from the table below, never anything a request supplies.
	column string
	window time.Duration
	title  string
	// phrase completes "<lot title> ...", given the window in words.
	phrase string
}

// Tightest window first. A lot that is already inside the last-call window the
// first time the job sees it should ring once, as a last call, rather than twice
// in the same tick — so the first stage that matches is the one that fires, and
// every wider stage is marked at the same time to say it has been overtaken.
var warnStages = []warnStage{
	{
		column: "last_call_alerted_at",
		window: lastCallWindow,
		title:  "BIDRL lot closing now",
		phrase: "closes in under %s — bid now or let it go",
	},
	{
		column: "ending_soon_alerted_at",
		window: endingSoonWindow,
		title:  "BIDRL lot ending soon",
		phrase: "closes within %s",
	},
}

type warnCandidate struct {
	id, title, ends string
	// alerted[i] is whether stage i has already fired for this lot.
	alerted []bool
}

func (p *Plugin) warnJob(jc hostjobs.Context) error {
	h, ok := p.host()
	if !ok {
		return fmt.Errorf("bidrl: host is not initialized")
	}
	now := h.Clock().Now()

	cols := make([]string, 0, len(warnStages))
	for _, s := range warnStages {
		cols = append(cols, "l."+s.column)
	}
	// Saved lots with a known close time that still have a warning left to give.
	// A lot every stage has already warned about is excluded in SQL rather than
	// loaded and skipped.
	var unfired []string
	for _, s := range warnStages {
		unfired = append(unfired, "l."+s.column+" = ''")
	}
	rows, err := h.Store().Query(jc, `
		SELECT l.id, l.title, l.ends_at, `+strings.Join(cols, ", ")+`
		  FROM bidrl_lots l
		 WHERE l.ends_at != ''
		   AND (`+strings.Join(unfired, " OR ")+`)
		   AND EXISTS (SELECT 1 FROM bidrl_favorites f WHERE f.lot_id = l.id)`)
	if err != nil {
		return err
	}
	var lots []warnCandidate
	for rows.Next() {
		c := warnCandidate{alerted: make([]bool, len(warnStages))}
		marks := make([]string, len(warnStages))
		dest := []any{&c.id, &c.title, &c.ends}
		for i := range marks {
			dest = append(dest, &marks[i])
		}
		if err := rows.Scan(dest...); err != nil {
			rows.Close()
			return err
		}
		for i, m := range marks {
			c.alerted[i] = m != ""
		}
		lots = append(lots, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}

	marked := now.UTC().Format(time.RFC3339Nano)
	for _, c := range lots {
		stage, ok := dueStage(c, now)
		if !ok {
			continue
		}
		s := warnStages[stage]
		body := c.title + " " + fmt.Sprintf(s.phrase, humanWindow(s.window))
		// Every column this firing settles: the stage itself, plus the wider stages
		// it overtook. Guarding on the fired column keeps a retry from re-alerting.
		set := make([]string, 0, len(warnStages)-stage)
		for i := stage; i < len(warnStages); i++ {
			set = append(set, warnStages[i].column+" = ?")
		}
		args := make([]any, 0, len(set)+2)
		for range set {
			args = append(args, marked)
		}
		args = append(args, c.id)
		if err := h.Store().Tx(jc, func(tx hoststorage.Tx) error {
			result, err := tx.Exec(jc, `UPDATE bidrl_lots SET `+strings.Join(set, ", ")+
				` WHERE id = ? AND `+s.column+` = ''`, args...)
			if err != nil {
				return err
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if affected == 0 {
				// Another worker won the stage between the candidate query and this
				// transaction. Do not publish a duplicate alert.
				return nil
			}
			return h.Events().PublishTx(jc, tx, "alert", body, alerted{
				Title: s.title, Body: body, LotID: c.id, EndsAt: c.ends,
			})
		}); err != nil {
			return err
		}
	}
	return nil
}

// dueStage picks the tightest stage whose window this lot has entered and which
// has not already fired for it.
func dueStage(c warnCandidate, now time.Time) (int, bool) {
	for i, s := range warnStages {
		if c.alerted[i] {
			continue
		}
		if endingWithin(c.ends, now, s.window) {
			return i, true
		}
	}
	return 0, false
}

// humanWindow says a warning window the way the alert should read it.
func humanWindow(d time.Duration) string {
	if d >= time.Hour {
		h := int(d / time.Hour)
		return fmt.Sprintf("%d hour%s", h, plural(h))
	}
	m := int(d / time.Minute)
	return fmt.Sprintf("%d minute%s", m, plural(m))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
