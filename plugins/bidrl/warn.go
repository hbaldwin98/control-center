package bidrl

import (
	"fmt"
	"time"

	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

func (p *Plugin) warnJob(jc hostjobs.Context) error {
	h, ok := p.host()
	if !ok {
		return fmt.Errorf("bidrl: host is not initialized")
	}
	now := h.Clock().Now()
	rows, err := h.Store().Query(jc, `
		SELECT l.id, l.title, l.ends_at
		  FROM bidrl_lots l
		 WHERE l.ending_soon_alerted_at = ''
		   AND l.ends_at != ''
		   AND EXISTS (SELECT 1 FROM bidrl_favorites f WHERE f.lot_id = l.id)`)
	if err != nil {
		return err
	}
	type candidate struct {
		id, title, ends string
	}
	var lots []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.title, &c.ends); err != nil {
			rows.Close()
			return err
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
		if !endingSoon(c.ends, now) {
			continue
		}
		body := fmt.Sprintf("%s closes within 24 hours", c.title)
		if err := h.Store().Tx(jc, func(tx hoststorage.Tx) error {
			if _, err := tx.Exec(jc, `UPDATE bidrl_lots SET ending_soon_alerted_at = ? WHERE id = ? AND ending_soon_alerted_at = ''`,
				marked, c.id); err != nil {
				return err
			}
			return h.Events().PublishTx(jc, tx, "alert", body, alerted{
				Title: "BIDRL lot ending soon", Body: body, LotID: c.id, EndsAt: c.ends,
			})
		}); err != nil {
			return err
		}
	}
	return nil
}
