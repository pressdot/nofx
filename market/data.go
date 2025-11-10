package market

import (
	"encoding/json"
	"fmt"
	"log"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
)

const (
	maxVPVRTrades   = 5000
	defaultVPVRBins = 24
)

// Get 获取指定代币的市场数据
func Get(symbol string) (*Data, error) {
	var klines3m, klines4h []Kline
	var err error
	// 标准化symbol
	symbol = Normalize(symbol)
	// 获取3分钟K线数据 (最近10个)
	klines3m, err = WSMonitorCli.GetCurrentKlines(symbol, "3m") // 多获取一些用于计算
	if err != nil {
		return nil, fmt.Errorf("获取3分钟K线失败: %v", err)
	}

	// 获取4小时K线数据 (最近10个)
	klines4h, err = WSMonitorCli.GetCurrentKlines(symbol, "4h") // 多获取用于计算指标
	if err != nil {
		return nil, fmt.Errorf("获取4小时K线失败: %v", err)
	}

	// 计算当前指标 (基于3分钟最新数据)
	currentPrice := klines3m[len(klines3m)-1].Close
	currentEMA20 := calculateEMA(klines3m, 20)
	currentMACD := calculateMACD(klines3m)
	currentRSI7 := calculateRSI(klines3m, 7)

	// 计算价格变化百分比
	// 1小时价格变化 = 20个3分钟K线前的价格
	priceChange1h := 0.0
	if len(klines3m) >= 21 { // 至少需要21根K线 (当前 + 20根前)
		price1hAgo := klines3m[len(klines3m)-21].Close
		if price1hAgo > 0 {
			priceChange1h = ((currentPrice - price1hAgo) / price1hAgo) * 100
		}
	}

	// 4小时价格变化 = 1个4小时K线前的价格
	priceChange4h := 0.0
	if len(klines4h) >= 2 {
		price4hAgo := klines4h[len(klines4h)-2].Close
		if price4hAgo > 0 {
			priceChange4h = ((currentPrice - price4hAgo) / price4hAgo) * 100
		}
	}

	// 获取OI数据
	oiData, err := getOpenInterestData(symbol)
	if err != nil {
		// OI失败不影响整体,使用默认值
		oiData = &OIData{Latest: 0, Average: 0}
	}

	// 获取Funding Rate
	fundingRate, _ := getFundingRate(symbol)

	// 计算日内系列数据
	intradayData := calculateIntradaySeries(klines3m)

	// 计算长期数据
	longerTermData := calculateLongerTermData(klines4h)

	// 计算VPVR数据（优先使用交易明细）
	apiClient := NewAPIClient()
	vpvrTrades4h, tradeErr := fetchTradesForVPVR(apiClient, symbol, klines4h, defaultVPVRBins)
	if tradeErr != nil {
		log.Printf("获取4h VPVR交易数据失败: %v", tradeErr)
	}
	vpvr4h := calculateVPVR(klines4h, vpvrTrades4h, defaultVPVRBins)
	if vpvr4h == nil {
		vpvr4h = calculateVPVR(klines4h, nil, defaultVPVRBins)
	}

	vpvrTrades3m, tradeErr := fetchTradesForVPVR(apiClient, symbol, klines3m, defaultVPVRBins)
	if tradeErr != nil {
		log.Printf("获取3m VPVR交易数据失败: %v", tradeErr)
	}
	vpvr3m := calculateVPVR(klines3m, vpvrTrades3m, defaultVPVRBins)
	if vpvr3m == nil {
		vpvr3m = calculateVPVR(klines3m, nil, defaultVPVRBins)
	}

	return &Data{
		Symbol:            symbol,
		CurrentPrice:      currentPrice,
		PriceChange1h:     priceChange1h,
		PriceChange4h:     priceChange4h,
		CurrentEMA20:      currentEMA20,
		CurrentMACD:       currentMACD,
		CurrentRSI7:       currentRSI7,
		OpenInterest:      oiData,
		FundingRate:       fundingRate,
		IntradaySeries:    intradayData,
		LongerTermContext: longerTermData,
		VPVR3m:            vpvr3m,
		VPVR4h:            vpvr4h,
	}, nil
}

// calculateEMA 计算EMA
func calculateEMA(klines []Kline, period int) float64 {
	if len(klines) < period {
		return 0
	}

	// 计算SMA作为初始EMA
	sum := 0.0
	for i := 0; i < period; i++ {
		sum += klines[i].Close
	}
	ema := sum / float64(period)

	// 计算EMA
	multiplier := 2.0 / float64(period+1)
	for i := period; i < len(klines); i++ {
		ema = (klines[i].Close-ema)*multiplier + ema
	}

	return ema
}

// calculateMACD 计算MACD
func calculateMACD(klines []Kline) float64 {
	if len(klines) < 26 {
		return 0
	}

	// 计算12期和26期EMA
	ema12 := calculateEMA(klines, 12)
	ema26 := calculateEMA(klines, 26)

	// MACD = EMA12 - EMA26
	return ema12 - ema26
}

// calculateRSI 计算RSI
func calculateRSI(klines []Kline, period int) float64 {
	if len(klines) <= period {
		return 0
	}

	gains := 0.0
	losses := 0.0

	// 计算初始平均涨跌幅
	for i := 1; i <= period; i++ {
		change := klines[i].Close - klines[i-1].Close
		if change > 0 {
			gains += change
		} else {
			losses += -change
		}
	}

	avgGain := gains / float64(period)
	avgLoss := losses / float64(period)

	// 使用Wilder平滑方法计算后续RSI
	for i := period + 1; i < len(klines); i++ {
		change := klines[i].Close - klines[i-1].Close
		if change > 0 {
			avgGain = (avgGain*float64(period-1) + change) / float64(period)
			avgLoss = (avgLoss * float64(period-1)) / float64(period)
		} else {
			avgGain = (avgGain * float64(period-1)) / float64(period)
			avgLoss = (avgLoss*float64(period-1) + (-change)) / float64(period)
		}
	}

	if avgLoss == 0 {
		return 100
	}

	rs := avgGain / avgLoss
	rsi := 100 - (100 / (1 + rs))

	return rsi
}

// calculateATR 计算ATR
func calculateATR(klines []Kline, period int) float64 {
	if len(klines) <= period {
		return 0
	}

	trs := make([]float64, len(klines))
	for i := 1; i < len(klines); i++ {
		high := klines[i].High
		low := klines[i].Low
		prevClose := klines[i-1].Close

		tr1 := high - low
		tr2 := math.Abs(high - prevClose)
		tr3 := math.Abs(low - prevClose)

		trs[i] = math.Max(tr1, math.Max(tr2, tr3))
	}

	// 计算初始ATR
	sum := 0.0
	for i := 1; i <= period; i++ {
		sum += trs[i]
	}
	atr := sum / float64(period)

	// Wilder平滑
	for i := period + 1; i < len(klines); i++ {
		atr = (atr*float64(period-1) + trs[i]) / float64(period)
	}

	return atr
}

// calculateIntradaySeries 计算日内系列数据
func calculateIntradaySeries(klines []Kline) *IntradayData {
	data := &IntradayData{
		MidPrices:   make([]float64, 0, 10),
		EMA20Values: make([]float64, 0, 10),
		MACDValues:  make([]float64, 0, 10),
		RSI7Values:  make([]float64, 0, 10),
		RSI14Values: make([]float64, 0, 10),
	}

	// 获取最近10个数据点
	start := len(klines) - 10
	if start < 0 {
		start = 0
	}

	for i := start; i < len(klines); i++ {
		data.MidPrices = append(data.MidPrices, klines[i].Close)

		// 计算每个点的EMA20
		if i >= 19 {
			ema20 := calculateEMA(klines[:i+1], 20)
			data.EMA20Values = append(data.EMA20Values, ema20)
		}

		// 计算每个点的MACD
		if i >= 25 {
			macd := calculateMACD(klines[:i+1])
			data.MACDValues = append(data.MACDValues, macd)
		}

		// 计算每个点的RSI
		if i >= 7 {
			rsi7 := calculateRSI(klines[:i+1], 7)
			data.RSI7Values = append(data.RSI7Values, rsi7)
		}
		if i >= 14 {
			rsi14 := calculateRSI(klines[:i+1], 14)
			data.RSI14Values = append(data.RSI14Values, rsi14)
		}
	}

	return data
}

// calculateLongerTermData 计算长期数据
func calculateLongerTermData(klines []Kline) *LongerTermData {
	data := &LongerTermData{
		MACDValues:  make([]float64, 0, 10),
		RSI14Values: make([]float64, 0, 10),
	}

	// 计算EMA
	data.EMA20 = calculateEMA(klines, 20)
	data.EMA50 = calculateEMA(klines, 50)

	// 计算ATR
	data.ATR3 = calculateATR(klines, 3)
	data.ATR14 = calculateATR(klines, 14)

	// 计算成交量
	if len(klines) > 0 {
		data.CurrentVolume = klines[len(klines)-1].Volume
		// 计算平均成交量
		sum := 0.0
		for _, k := range klines {
			sum += k.Volume
		}
		data.AverageVolume = sum / float64(len(klines))
	}

	// 计算MACD和RSI序列
	start := len(klines) - 10
	if start < 0 {
		start = 0
	}

	for i := start; i < len(klines); i++ {
		if i >= 25 {
			macd := calculateMACD(klines[:i+1])
			data.MACDValues = append(data.MACDValues, macd)
		}
		if i >= 14 {
			rsi14 := calculateRSI(klines[:i+1], 14)
			data.RSI14Values = append(data.RSI14Values, rsi14)
		}
	}

	return data
}

// getOpenInterestData 获取OI数据
func getOpenInterestData(symbol string) (*OIData, error) {
	url := fmt.Sprintf("https://fapi.binance.com/fapi/v1/openInterest?symbol=%s", symbol)

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		OpenInterest string `json:"openInterest"`
		Symbol       string `json:"symbol"`
		Time         int64  `json:"time"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	oi, _ := strconv.ParseFloat(result.OpenInterest, 64)

	return &OIData{
		Latest:  oi,
		Average: oi * 0.999, // 近似平均值
	}, nil
}

// getFundingRate 获取资金费率
func getFundingRate(symbol string) (float64, error) {
	url := fmt.Sprintf("https://fapi.binance.com/fapi/v1/premiumIndex?symbol=%s", symbol)

	resp, err := http.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var result struct {
		Symbol          string `json:"symbol"`
		MarkPrice       string `json:"markPrice"`
		IndexPrice      string `json:"indexPrice"`
		LastFundingRate string `json:"lastFundingRate"`
		NextFundingTime int64  `json:"nextFundingTime"`
		InterestRate    string `json:"interestRate"`
		Time            int64  `json:"time"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return 0, err
	}

	rate, _ := strconv.ParseFloat(result.LastFundingRate, 64)
	return rate, nil
}

// Format 格式化输出市场数据
func Format(data *Data) string {
	var sb strings.Builder

	// 使用动态精度格式化价格
	priceStr := formatPriceWithDynamicPrecision(data.CurrentPrice)
	sb.WriteString(fmt.Sprintf("current_price = %s, current_ema20 = %.3f, current_macd = %.3f, current_rsi (7 period) = %.3f\n\n",
		priceStr, data.CurrentEMA20, data.CurrentMACD, data.CurrentRSI7))

	sb.WriteString(fmt.Sprintf("In addition, here is the latest %s open interest and funding rate for perps:\n\n",
		data.Symbol))

	if data.OpenInterest != nil {
		// 使用动态精度格式化 OI 数据
		oiLatestStr := formatPriceWithDynamicPrecision(data.OpenInterest.Latest)
		oiAverageStr := formatPriceWithDynamicPrecision(data.OpenInterest.Average)
		sb.WriteString(fmt.Sprintf("Open Interest: Latest: %s Average: %s\n\n",
			oiLatestStr, oiAverageStr))
	}

	sb.WriteString(fmt.Sprintf("Funding Rate: %.2e\n\n", data.FundingRate))

	if data.IntradaySeries != nil {
		sb.WriteString("Intraday series (3‑minute intervals, oldest → latest):\n\n")

		if len(data.IntradaySeries.MidPrices) > 0 {
			sb.WriteString(fmt.Sprintf("Mid prices: %s\n\n", formatFloatSlice(data.IntradaySeries.MidPrices)))
		}

		if len(data.IntradaySeries.EMA20Values) > 0 {
			sb.WriteString(fmt.Sprintf("EMA indicators (20‑period): %s\n\n", formatFloatSlice(data.IntradaySeries.EMA20Values)))
		}

		if len(data.IntradaySeries.MACDValues) > 0 {
			sb.WriteString(fmt.Sprintf("MACD indicators: %s\n\n", formatFloatSlice(data.IntradaySeries.MACDValues)))
		}

		if len(data.IntradaySeries.RSI7Values) > 0 {
			sb.WriteString(fmt.Sprintf("RSI indicators (7‑Period): %s\n\n", formatFloatSlice(data.IntradaySeries.RSI7Values)))
		}

		if len(data.IntradaySeries.RSI14Values) > 0 {
			sb.WriteString(fmt.Sprintf("RSI indicators (14‑Period): %s\n\n", formatFloatSlice(data.IntradaySeries.RSI14Values)))
		}
	}

	if data.LongerTermContext != nil {
		sb.WriteString("Longer‑term context (4‑hour timeframe):\n\n")

		sb.WriteString(fmt.Sprintf("20‑Period EMA: %.3f vs. 50‑Period EMA: %.3f\n\n",
			data.LongerTermContext.EMA20, data.LongerTermContext.EMA50))

		sb.WriteString(fmt.Sprintf("3‑Period ATR: %.3f vs. 14‑Period ATR: %.3f\n\n",
			data.LongerTermContext.ATR3, data.LongerTermContext.ATR14))

		sb.WriteString(fmt.Sprintf("Current Volume: %.3f vs. Average Volume: %.3f\n\n",
			data.LongerTermContext.CurrentVolume, data.LongerTermContext.AverageVolume))

		if len(data.LongerTermContext.MACDValues) > 0 {
			sb.WriteString(fmt.Sprintf("MACD indicators: %s\n\n", formatFloatSlice(data.LongerTermContext.MACDValues)))
		}

		if len(data.LongerTermContext.RSI14Values) > 0 {
			sb.WriteString(fmt.Sprintf("RSI indicators (14‑Period): %s\n\n", formatFloatSlice(data.LongerTermContext.RSI14Values)))
		}
	}

	if data.VPVR3m != nil && len(data.VPVR3m.PriceLevels) > 0 {
		sb.WriteString("VPVR (3-minute visible range):\n\n")
		sb.WriteString(fmt.Sprintf("Point of Control: %.3f\n", data.VPVR3m.POC))
		sb.WriteString(fmt.Sprintf("Value Area Low: %.3f, Value Area High: %.3f\n\n", data.VPVR3m.VAL, data.VPVR3m.VAH))
		sb.WriteString(fmt.Sprintf("Price levels: %s\n\n", formatFloatSlice(data.VPVR3m.PriceLevels)))
		sb.WriteString(fmt.Sprintf("Volume profile: %s\n\n", formatFloatSlice(data.VPVR3m.Volumes)))
		if len(data.VPVR3m.Trades) > 0 {
			sb.WriteString(fmt.Sprintf("Trades analyzed: %d\n\n", len(data.VPVR3m.Trades)))
		}
	}

	if data.VPVR4h != nil && len(data.VPVR4h.PriceLevels) > 0 {
		sb.WriteString("VPVR (4-hour visible range):\n\n")
		sb.WriteString(fmt.Sprintf("Point of Control: %.3f\n", data.VPVR4h.POC))
		sb.WriteString(fmt.Sprintf("Value Area Low: %.3f, Value Area High: %.3f\n\n", data.VPVR4h.VAL, data.VPVR4h.VAH))
		sb.WriteString(fmt.Sprintf("Price levels: %s\n\n", formatFloatSlice(data.VPVR4h.PriceLevels)))
		sb.WriteString(fmt.Sprintf("Volume profile: %s\n\n", formatFloatSlice(data.VPVR4h.Volumes)))
		if len(data.VPVR4h.Trades) > 0 {
			sb.WriteString(fmt.Sprintf("Trades analyzed: %d\n\n", len(data.VPVR4h.Trades)))
		}
	}

	return sb.String()
}

// formatPriceWithDynamicPrecision 根据价格区间动态选择精度
// 这样可以完美支持从超低价 meme coin (< 0.0001) 到 BTC/ETH 的所有币种
func formatPriceWithDynamicPrecision(price float64) string {
	switch {
	case price < 0.0001:
		// 超低价 meme coin: 1000SATS, 1000WHY, DOGS
		// 0.00002070 → "0.00002070" (8位小数)
		return fmt.Sprintf("%.8f", price)
	case price < 0.001:
		// 低价 meme coin: NEIRO, HMSTR, HOT, NOT
		// 0.00015060 → "0.000151" (6位小数)
		return fmt.Sprintf("%.6f", price)
	case price < 0.01:
		// 中低价币: PEPE, SHIB, MEME
		// 0.00556800 → "0.005568" (6位小数)
		return fmt.Sprintf("%.6f", price)
	case price < 1.0:
		// 低价币: ASTER, DOGE, ADA, TRX
		// 0.9954 → "0.9954" (4位小数)
		return fmt.Sprintf("%.4f", price)
	case price < 100:
		// 中价币: SOL, AVAX, LINK, MATIC
		// 23.4567 → "23.4567" (4位小数)
		return fmt.Sprintf("%.4f", price)
	default:
		// 高价币: BTC, ETH (节省 Token)
		// 45678.9123 → "45678.91" (2位小数)
		return fmt.Sprintf("%.2f", price)
	}
}

// formatFloatSlice 格式化float64切片为字符串（使用动态精度）
func formatFloatSlice(values []float64) string {
	strValues := make([]string, len(values))
	for i, v := range values {
		strValues[i] = formatPriceWithDynamicPrecision(v)
	}
	return "[" + strings.Join(strValues, ", ") + "]"
}

// Normalize 标准化symbol,确保是USDT交易对
func Normalize(symbol string) string {
	symbol = strings.ToUpper(symbol)
	if strings.HasSuffix(symbol, "USDT") {
		return symbol
	}
	return symbol + "USDT"
}

// parseFloat 解析float值
func parseFloat(v interface{}) (float64, error) {
	switch val := v.(type) {
	case string:
		return strconv.ParseFloat(val, 64)
	case float64:
		return val, nil
	case int:
		return float64(val), nil
	case int64:
		return float64(val), nil
	default:
		return 0, fmt.Errorf("unsupported type: %T", v)
	}
}

// calculateVPVR 构建可见区间成交量分布（优先使用交易明细，退路为K线成交量）
func calculateVPVR(klines []Kline, trades []Trade, numBins int) *VPVRData {
	if len(klines) == 0 || numBins <= 0 {
		return nil
	}

	// 优先使用真实成交数据构建价格区间，避免异常极值导致分布被压扁
	if len(trades) > 0 {
		tradeMin, tradeMax := tradePriceRange(trades)
		if tradeMax > tradeMin {
			priceMin, priceMax := expandPriceRange(tradeMin, tradeMax)
			priceLevels, binWidth := buildVPVRPriceLevels(priceMin, priceMax, numBins)
			if binWidth > 0 {
				volumes := accumulateVolumesFromTrades(trades, priceMin, binWidth, numBins)
				if countNonZeroBins(volumes) > 1 {
					result := finalizeVPVR(priceLevels, volumes, priceMin, binWidth)
					if result != nil {
						result.Trades = trades
					}
					return result
				}
			}
		}
	}

	// 如果成交数据不足或分布异常，回退到K线成交量估算
	priceMin, priceMax := vpvrPriceRange(klines, numBins)
	if priceMin == math.MaxFloat64 || priceMax <= priceMin {
		return nil
	}

	priceLevels, binWidth := buildVPVRPriceLevels(priceMin, priceMax, numBins)
	if binWidth == 0 {
		return nil
	}

	volumes := accumulateVolumesFromKlines(klines, priceMin, binWidth, numBins)
	return finalizeVPVR(priceLevels, volumes, priceMin, binWidth)
}

func fetchTradesForVPVR(client *APIClient, symbol string, klines []Kline, numBins int) ([]Trade, error) {
	if client == nil {
		return nil, fmt.Errorf("nil api client")
	}
	if len(klines) == 0 {
		return nil, nil
	}

	startIdx := len(klines) - numBins
	if startIdx < 0 {
		startIdx = 0
	}

	startTime := klines[startIdx].OpenTime
	endTime := klines[len(klines)-1].CloseTime
	if endTime <= startTime {
		return nil, fmt.Errorf("invalid VPVR trade window")
	}

	return client.GetAggregatedTrades(symbol, startTime, endTime, maxVPVRTrades)
}

func vpvrPriceRange(klines []Kline, numBins int) (float64, float64) {
	priceMin := math.MaxFloat64
	priceMax := -math.MaxFloat64

	start := len(klines) - numBins
	if start < 0 {
		start = 0
	}

	for i := start; i < len(klines); i++ {
		if klines[i].Low < priceMin {
			priceMin = klines[i].Low
		}
		if klines[i].High > priceMax {
			priceMax = klines[i].High
		}
	}

	return priceMin, priceMax
}

func buildVPVRPriceLevels(priceMin, priceMax float64, numBins int) ([]float64, float64) {
	if priceMin == math.MaxFloat64 || priceMax == -math.MaxFloat64 {
		return nil, 0
	}
	if priceMax == priceMin {
		// 扩展极小范围以避免分母为零
		adjustment := math.Max(math.Abs(priceMin)*1e-4, 1e-6)
		priceMax = priceMin + adjustment
	}

	binWidth := (priceMax - priceMin) / float64(numBins)
	if binWidth == 0 {
		return nil, 0
	}

	priceLevels := make([]float64, numBins)
	for i := 0; i < numBins; i++ {
		priceLevels[i] = priceMin + (float64(i)+0.5)*binWidth
	}

	return priceLevels, binWidth
}

func accumulateVolumesFromTrades(trades []Trade, priceMin, binWidth float64, numBins int) []float64 {
	volumes := make([]float64, numBins)
	if numBins == 0 || binWidth == 0 {
		return volumes
	}

	upperBound := priceMin + float64(numBins)*binWidth

	for _, trade := range trades {
		price := trade.Price
		if price < priceMin {
			price = priceMin
		}
		if price >= upperBound {
			price = math.Nextafter(upperBound, priceMin)
		}

		idx := binIndex(price, priceMin, binWidth, numBins)
		volumes[idx] += trade.Quantity
	}

	return volumes
}

func accumulateVolumesFromKlines(klines []Kline, priceMin, binWidth float64, numBins int) []float64 {
	volumes := make([]float64, numBins)
	if numBins == 0 || binWidth == 0 {
		return volumes
	}

	upperBound := priceMin + float64(numBins)*binWidth
	start := len(klines) - numBins
	if start < 0 {
		start = 0
	}

	for i := start; i < len(klines); i++ {
		typicalPrice := (klines[i].High + klines[i].Low + klines[i].Close) / 3
		if typicalPrice < priceMin {
			typicalPrice = priceMin
		}
		if typicalPrice >= upperBound {
			typicalPrice = math.Nextafter(upperBound, priceMin)
		}

		idx := binIndex(typicalPrice, priceMin, binWidth, numBins)
		volumes[idx] += klines[i].Volume
	}

	return volumes
}

func tradePriceRange(trades []Trade) (float64, float64) {
	priceMin := math.MaxFloat64
	priceMax := -math.MaxFloat64

	for _, trade := range trades {
		if trade.Price < priceMin {
			priceMin = trade.Price
		}
		if trade.Price > priceMax {
			priceMax = trade.Price
		}
	}

	return priceMin, priceMax
}

func expandPriceRange(priceMin, priceMax float64) (float64, float64) {
	if priceMax <= priceMin {
		return priceMin, priceMax
	}

	span := priceMax - priceMin
	padding := math.Max(span*0.005, 1e-6)
	return priceMin - padding, priceMax + padding
}

func countNonZeroBins(volumes []float64) int {
	count := 0
	for _, v := range volumes {
		if v > 0 {
			count++
		}
	}
	return count
}

func binIndex(price, priceMin, binWidth float64, numBins int) int {
	idx := int(math.Floor((price - priceMin) / binWidth))
	if idx < 0 {
		idx = 0
	}
	if idx >= numBins {
		idx = numBins - 1
	}
	return idx
}

func finalizeVPVR(priceLevels, volumes []float64, priceMin, binWidth float64) *VPVRData {
	if len(priceLevels) == 0 || len(volumes) == 0 {
		return &VPVRData{
			PriceLevels: priceLevels,
			Volumes:     volumes,
		}
	}

	totalVolume := 0.0
	for _, v := range volumes {
		totalVolume += v
	}

	result := &VPVRData{
		PriceLevels: priceLevels,
		Volumes:     volumes,
	}

	if totalVolume == 0 {
		return result
	}

	pocIndex := 0
	maxVolume := volumes[0]
	for i := 1; i < len(volumes); i++ {
		if volumes[i] > maxVolume {
			maxVolume = volumes[i]
			pocIndex = i
		}
	}

	included := make([]bool, len(volumes))
	included[pocIndex] = true
	cumulative := volumes[pocIndex]
	left := pocIndex - 1
	right := pocIndex + 1
	targetVolume := totalVolume * 0.7

	for cumulative < targetVolume && (left >= 0 || right < len(volumes)) {
		leftVolume := -1.0
		if left >= 0 {
			leftVolume = volumes[left]
		}

		rightVolume := -1.0
		if right < len(volumes) {
			rightVolume = volumes[right]
		}

		switch {
		case rightVolume > leftVolume:
			if right < len(volumes) {
				included[right] = true
				cumulative += volumes[right]
			}
			right++
		case left >= 0:
			included[left] = true
			cumulative += volumes[left]
			left--
		case right < len(volumes):
			included[right] = true
			cumulative += volumes[right]
			right++
		default:
			left = -1
			right = len(volumes)
		}
	}

	valIndex := pocIndex
	vahIndex := pocIndex
	for i, ok := range included {
		if !ok {
			continue
		}
		if i < valIndex {
			valIndex = i
		}
		if i > vahIndex {
			vahIndex = i
		}
	}

	result.POC = priceLevels[pocIndex]
	result.VAL = priceMin + float64(valIndex)*binWidth
	result.VAH = priceMin + float64(vahIndex+1)*binWidth

	return result
}
