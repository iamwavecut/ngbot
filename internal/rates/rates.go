package rates

import (
	"context"
	"encoding/json"
	"github.com/davecgh/go-spew/spew"
	log "github.com/sirupsen/logrus"
	"io/ioutil"
	"net/http"
	"sync"
	"time"
)

const cookieName = "ASP.NET_SessionId"

type (
	rates struct {
		lastRatePoint point
		checkDate     time.Time
		checkInterval time.Duration
		cookie        *http.Cookie
	}
	rate struct {
		InstrumentName       string  `json:"instrumentName"`
		TradeReportsURL      string  `json:"tradeReportsUrl"`
		InstrumentCurrencyID int     `json:"instrumentCurrencyId"`
		TradeStartDate       string  `json:"tradeStartDate"`
		TradeEndDate         string  `json:"tradeEndDate"`
		Points               []point `json:"points"`
	}
	point struct {
		Date                 time.Time `json:"date"`
		Rate                 float64   `json:"rate"`
		RateDeviation        float64   `json:"rateDeviation"`
		RateDeviationPercent float64   `json:"rateDeviationPercent"`
	}
)

var (
	instance        *rates
	once            sync.Once
	activeDaysHours = map[time.Weekday][2]int{
		time.Monday:    {9, 13},
		time.Tuesday:   {9, 13},
		time.Wednesday: {9, 13},
		time.Saturday:  {9, 13},
		time.Friday:    {9, 23},
	}
)

func Get() *rates {
	once.Do(func() {
		instance = &rates{
			checkInterval: time.Minute * 2,
		}
		instance.getCookie()
	})
	return instance
}

func (r *rates) SetInterval(duration time.Duration) {
	r.checkInterval = duration
}

func (r *rates) RunContext(ctx context.Context) {
	l := r.getEntry()
	client := &http.Client{}
	t := time.NewTicker(r.checkInterval)

	tick := func(now time.Time) bool {
		hours, ok := activeDaysHours[now.Weekday()]
		if !ok {
			l.Trace("not active day")
			return false
		}

		if now.Hour() <= hours[0] || now.Hour() >= hours[1] {
			l.Trace("not active hour")
			return false
		}

		if r.cookie == nil {
			r.getCookie()
			return false
		}

		ctxTimeout, cancel := context.WithTimeout(ctx, 30*time.Second)

		rq, err := http.NewRequestWithContext(ctxTimeout, "GET", "https://www.bcse.by/ru/currencymarket/rateschart", nil)
		if err != nil {
			l.WithError(err).Error("cant create request")
			r.cookie = nil
			cancel()
			return false
		}
		q := rq.URL.Query()
		q.Add("instrumentId", "1077")
		q.Add("lastTradeDate", now.Format("01/02/2006 15:04"))
		q.Add("mode", "ContinuousDoubleAuction")
		q.Add("payTerm", "0")
		q.Add("", "")

		h := rq.Header
		h.Add("Accept", "*/*")
		h.Add("X-Requested-With", "XMLHttpRequest")
		h.Add("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/80.0.3987.132 Safari/537.36")
		h.Add("Referer", "https://www.bcse.by/")

		rq.AddCookie(r.cookie)

		spew.Dump(rq)

		resp, err := client.Do(rq)
		if err != nil {
			l.WithError(err).Error("cant do request")
			cancel()
			return false
		}
		l.Trace("got get resp ", resp.Status)

		body, err := ioutil.ReadAll(resp.Body)
		l.Trace(string(body))
		if err != nil {
			l.WithError(err).Error("cant read body")
		}

		rate := rate{}
		if err = json.Unmarshal(body, &rate); err != nil {
			l.WithError(err).Error("cant unmarshal body")
		}
		l.Trace("got points ", len(rate.Points))
		if len(rate.Points) == 0 {
			return true
		}

		freshPoint := rate.Points[len(rate.Points)-1]
		if freshPoint != r.lastRatePoint {
			r.lastRatePoint = freshPoint
			r.checkDate = now
		}

		resp.Body.Close()
		cancel()
		return true
	}

	go func() {
		tick(time.Now())
		l.Info(r.lastRatePoint)

		for {
			time.Sleep(time.Second)
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				if !tick(now) {
					continue
				}
				l.Info(r.lastRatePoint)
			}
		}
	}()
}

func (r *rates) getCookie() {
	l := r.getEntry()
	resp, err := http.Head("https://www.bcse.by/")
	if err != nil {
		l.WithError(err).Error("cant get cookie")
	}
	l.Trace("got head resp ", resp.Status)

	for _, c := range resp.Cookies() {
		l.Trace("got cookie ", c.Name)
		if c.Name == cookieName {
			l.Trace("use ", c.Name)
			r.cookie = c
		}
	}
}

func (r *rates) getEntry() *log.Entry {
	return log.WithField("context", "rates")
}
