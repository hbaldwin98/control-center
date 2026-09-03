package bidrl

import ()

// What an auction and a lot look like on the wire, and how a row becomes one.

type auctionView struct {
	ID            string `json:"id"`
	URL           string `json:"url"`
	Title         string `json:"title"`
	Status        string `json:"status"`
	LotCount      int    `json:"lotCount"`
	LastError     string `json:"lastError"`
	CollectedAt   string `json:"collectedAt"`
	EndsAt        string `json:"endsAt"`
	AffiliateID   string `json:"affiliateId"`
	AffiliateName string `json:"affiliateName"`
	City          string `json:"city"`
}

type lotView struct {
	ID              string   `json:"id"`
	AuctionID       string   `json:"auctionId"`
	URL             string   `json:"url"`
	LotCode         string   `json:"lotCode"`
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	CurrentBidCents *int64   `json:"currentBidCents"`
	MinBidCents     *int64   `json:"minBidCents"`
	IncrementCents  *int64   `json:"bidIncrementCents"`
	BidCount        int      `json:"bidCount"`
	HighBidder      string   `json:"highBidder"`
	EndsAt          string   `json:"endsAt"`
	BiddingExtended bool     `json:"biddingExtended"`
	ReserveMet      bool     `json:"reserveMet"`
	Category        string   `json:"category"`
	Bucket          string   `json:"bucket"`
	AffiliateID     string   `json:"affiliateId"`
	AffiliateName   string   `json:"affiliateName"`
	City            string   `json:"city"`
	Identification  string   `json:"identification"`
	Basis           string   `json:"basis"`
	ModelOrSKU      string   `json:"modelOrSku"`
	TitleAgreement  float64  `json:"titleAgreement"`
	MislabelScore   float64  `json:"mislabelScore"`
	PriceCents      *int64   `json:"priceCents"`
	PriceKind       string   `json:"priceKind"`
	SourceURL       string   `json:"sourceUrl"`
	CitedText       string   `json:"citedText"`
	SourceTitle     string   `json:"sourceTitle"`
	SourceClass     string   `json:"sourceClass"`
	SourceLabel     string   `json:"sourceLabel"`
	ReusedFromLotID string   `json:"reusedFromLotId"`
	RetrievedAt     string   `json:"retrievedAt"`
	DealScore       *float64 `json:"dealScore"`
	MatchScore      *float64 `json:"matchScore,omitempty"`
	MatchReason     string   `json:"matchReason,omitempty"`
	ThumbURL        string   `json:"thumbUrl"`
	PhotoURLs       []string `json:"photoUrls,omitempty"`
	Favorite        bool     `json:"favorite"`
	FavoriteNote    string   `json:"favoriteNote"`
	SavedAt         string   `json:"savedAt"`
}

func lotPayload(lot lotView, eventID int64) map[string]any {
	return map[string]any{
		"id": lot.ID, "auctionId": lot.AuctionID, "url": lot.URL, "lotCode": lot.LotCode, "title": lot.Title,
		"description": lot.Description, "currentBidCents": lot.CurrentBidCents, "minBidCents": lot.MinBidCents,
		"bidIncrementCents": lot.IncrementCents, "bidCount": lot.BidCount, "highBidder": lot.HighBidder,
		"endsAt": lot.EndsAt, "biddingExtended": lot.BiddingExtended, "reserveMet": lot.ReserveMet,
		"category": lot.Category, "bucket": lot.Bucket, "identification": lot.Identification, "basis": lot.Basis,
		"modelOrSku": lot.ModelOrSKU, "titleAgreement": lot.TitleAgreement, "mislabelScore": lot.MislabelScore,
		"priceCents": lot.PriceCents, "priceKind": lot.PriceKind, "sourceUrl": lot.SourceURL, "citedText": lot.CitedText,
		"sourceTitle": lot.SourceTitle, "sourceClass": lot.SourceClass, "sourceLabel": lot.SourceLabel,
		"reusedFromLotId": lot.ReusedFromLotID, "retrievedAt": lot.RetrievedAt, "dealScore": lot.DealScore,
		"thumbUrl": lot.ThumbURL, "photoUrls": lot.PhotoURLs, "latestEventId": eventID,
	}
}

func capLots(lots []lotView, n int) []lotView {
	if len(lots) > n {
		return lots[:n]
	}
	return lots
}
