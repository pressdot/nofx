package market

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"
)

const (
	baseURL = "https://fapi.binance.com"
)

type APIClient struct {
	client *http.Client
}

type aggTradeResponse struct {
	AggregateTradeID int64  `json:"a"`
	Price            string `json:"p"`
	Quantity         string `json:"q"`
	FirstTradeID     int64  `json:"f"`
	LastTradeID      int64  `json:"l"`
	Timestamp        int64  `json:"T"`
	IsBuyerMaker     bool   `json:"m"`
}

func NewAPIClient() *APIClient {
	return &APIClient{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *APIClient) GetExchangeInfo() (*ExchangeInfo, error) {
	url := fmt.Sprintf("%s/fapi/v1/exchangeInfo", baseURL)
	resp, err := c.client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var exchangeInfo ExchangeInfo
	err = json.Unmarshal(body, &exchangeInfo)
	if err != nil {
		return nil, err
	}

	return &exchangeInfo, nil
}

func (c *APIClient) GetKlines(symbol, interval string, limit int) ([]Kline, error) {
	url := fmt.Sprintf("%s/fapi/v1/klines", baseURL)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	q := req.URL.Query()
	q.Add("symbol", symbol)
	q.Add("interval", interval)
	q.Add("limit", strconv.Itoa(limit))
	req.URL.RawQuery = q.Encode()

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var klineResponses []KlineResponse
	err = json.Unmarshal(body, &klineResponses)
	if err != nil {
		return nil, err
	}

	var klines []Kline
	for _, kr := range klineResponses {
		kline, err := parseKline(kr)
		if err != nil {
			log.Printf("解析K线数据失败: %v", err)
			continue
		}
		klines = append(klines, kline)
	}

	return klines, nil
}

func parseKline(kr KlineResponse) (Kline, error) {
	var kline Kline

	if len(kr) < 11 {
		return kline, fmt.Errorf("invalid kline data")
	}

	// 解析各个字段
	kline.OpenTime = int64(kr[0].(float64))
	kline.Open, _ = strconv.ParseFloat(kr[1].(string), 64)
	kline.High, _ = strconv.ParseFloat(kr[2].(string), 64)
	kline.Low, _ = strconv.ParseFloat(kr[3].(string), 64)
	kline.Close, _ = strconv.ParseFloat(kr[4].(string), 64)
	kline.Volume, _ = strconv.ParseFloat(kr[5].(string), 64)
	kline.CloseTime = int64(kr[6].(float64))
	kline.QuoteVolume, _ = strconv.ParseFloat(kr[7].(string), 64)
	kline.Trades = int(kr[8].(float64))
	kline.TakerBuyBaseVolume, _ = strconv.ParseFloat(kr[9].(string), 64)
	kline.TakerBuyQuoteVolume, _ = strconv.ParseFloat(kr[10].(string), 64)

	return kline, nil
}

func (c *APIClient) GetCurrentPrice(symbol string) (float64, error) {
	url := fmt.Sprintf("%s/fapi/v1/ticker/price", baseURL)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, err
	}

	q := req.URL.Query()
	q.Add("symbol", symbol)
	req.URL.RawQuery = q.Encode()

	resp, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var ticker PriceTicker
	err = json.Unmarshal(body, &ticker)
	if err != nil {
		return 0, err
	}

	price, err := strconv.ParseFloat(ticker.Price, 64)
	if err != nil {
		return 0, err
	}

	return price, nil
}

// GetAggregatedTrades 获取指定时间范围内的聚合成交数据
func (c *APIClient) GetAggregatedTrades(symbol string, startTime, endTime int64, maxRecords int) ([]Trade, error) {
	if startTime <= 0 || endTime <= 0 || startTime >= endTime {
		return nil, fmt.Errorf("invalid time range for trades")
	}

	const batchLimit = 1000

	var (
		allTrades []Trade
		current   = startTime
	)

	for current < endTime {
		batch, err := c.fetchAggregatedTradesBatch(symbol, current, batchLimit)
		if err != nil {
			return nil, err
		}

		if len(batch) == 0 {
			break
		}

		for _, t := range batch {
			if t.Timestamp > endTime {
				break
			}
			allTrades = append(allTrades, t)
			if maxRecords > 0 && len(allTrades) >= maxRecords {
				return allTrades[:maxRecords], nil
			}
		}

		lastTS := batch[len(batch)-1].Timestamp
		nextStart := lastTS + 1
		if nextStart <= current {
			nextStart = current + 1
		}
		current = nextStart

		if len(batch) < batchLimit {
			break
		}
	}

	return allTrades, nil
}

func (c *APIClient) fetchAggregatedTradesBatch(symbol string, startTime int64, limit int) ([]Trade, error) {
	url := fmt.Sprintf("%s/fapi/v1/aggTrades", baseURL)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	q := req.URL.Query()
	q.Add("symbol", symbol)
	if startTime > 0 {
		q.Add("startTime", strconv.FormatInt(startTime, 10))
	}
	if limit > 0 {
		q.Add("limit", strconv.Itoa(limit))
	}
	req.URL.RawQuery = q.Encode()

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var rawTrades []aggTradeResponse
	if err := json.Unmarshal(body, &rawTrades); err != nil {
		return nil, err
	}

	trades := make([]Trade, 0, len(rawTrades))
	for _, rt := range rawTrades {
		price, err := strconv.ParseFloat(rt.Price, 64)
		if err != nil {
			log.Printf("解析交易价格失败: %v", err)
			continue
		}
		quantity, err := strconv.ParseFloat(rt.Quantity, 64)
		if err != nil {
			log.Printf("解析交易数量失败: %v", err)
			continue
		}

		trades = append(trades, Trade{
			TradeID:      rt.AggregateTradeID,
			Price:        price,
			Quantity:     quantity,
			Timestamp:    rt.Timestamp,
			IsBuyerMaker: rt.IsBuyerMaker,
		})
	}

	return trades, nil
}
