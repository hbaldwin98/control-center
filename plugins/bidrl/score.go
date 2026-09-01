package bidrl

// dealScore is (estimate - bid) / estimate. A high score is a cheap lot relative to
// the comparable. Missing either number yields no score.
func dealScore(bidCents *int64, estimateCents int64) (float64, bool) {
	if bidCents == nil || *bidCents < 0 || estimateCents <= 0 {
		return 0, false
	}
	return float64(estimateCents-*bidCents) / float64(estimateCents), true
}

func mislabelScore(titleAgreement float64) float64 {
	if titleAgreement < 0 {
		return 1
	}
	if titleAgreement > 1 {
		return 0
	}
	return 1 - titleAgreement
}
