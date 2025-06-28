package service

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/assimon/luuu/model/data"
	"github.com/assimon/luuu/model/request"
	"github.com/assimon/luuu/mq"
	"github.com/assimon/luuu/mq/handle"
	"github.com/assimon/luuu/telegram"
	"github.com/assimon/luuu/util/http_client"
	"github.com/assimon/luuu/util/json"
	"github.com/assimon/luuu/util/log"
	"github.com/gagliardetto/solana-go"
	"github.com/golang-module/carbon/v2"
	"github.com/gookit/goutil/stdutil"
	"github.com/hibiken/asynq"
	"github.com/shopspring/decimal"
)

const UsdtTrc20ApiUri = "https://apilist.tronscanapi.com/api/transfer/trc20"
const SolanaRpcUri = "https://api.mainnet-beta.solana.com"
const UsdtSolanaMint = "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB"

type UsdtTrc20Resp struct {
	PageSize int    `json:"page_size"`
	Code     int    `json:"code"`
	Data     []Data `json:"data"`
}

type TokenInfo struct {
	TokenID      string `json:"tokenId"`
	TokenAbbr    string `json:"tokenAbbr"`
	TokenName    string `json:"tokenName"`
	TokenDecimal int    `json:"tokenDecimal"`
	TokenCanShow int    `json:"tokenCanShow"`
	TokenType    string `json:"tokenType"`
	TokenLogo    string `json:"tokenLogo"`
	TokenLevel   string `json:"tokenLevel"`
	IssuerAddr   string `json:"issuerAddr"`
	Vip          bool   `json:"vip"`
}

type Data struct {
	Amount         string `json:"amount"`
	ApprovalAmount string `json:"approval_amount"`
	BlockTimestamp int64  `json:"block_timestamp"`
	Block          int    `json:"block"`
	From           string `json:"from"`
	To             string `json:"to"`
	Hash           string `json:"hash"`
	Confirmed      int    `json:"confirmed"`
	ContractType   string `json:"contract_type"`
	ContracTType   int    `json:"contractType"`
	Revert         int    `json:"revert"`
	ContractRet    string `json:"contract_ret"`
	EventType      string `json:"event_type"`
	IssueAddress   string `json:"issue_address"`
	Decimals       int    `json:"decimals"`
	TokenName      string `json:"token_name"`
	ID             string `json:"id"`
	Direction      int    `json:"direction"`
}

type SolanaSignature struct {
	Signature string `json:"signature"`
	BlockTime int64  `json:"blockTime"`
}

type SignaturesForAddressResp struct {
	Result []SolanaSignature `json:"result"`
}

type TokenBalance struct {
	Owner         string `json:"owner"`
	Mint          string `json:"mint"`
	UiTokenAmount struct {
		Amount   string `json:"amount"`
		Decimals int    `json:"decimals"`
	} `json:"uiTokenAmount"`
}

type SolanaTransactionResp struct {
	Result struct {
		BlockTime int64 `json:"blockTime"`
		Meta      struct {
			Err               interface{}    `json:"err"`
			PreTokenBalances  []TokenBalance `json:"preTokenBalances"`
			PostTokenBalances []TokenBalance `json:"postTokenBalances"`
		} `json:"meta"`
		Transaction struct {
			Signatures []string `json:"signatures"`
		} `json:"transaction"`
	} `json:"result"`
}

// Trc20CallBack trc20回调
func Trc20CallBack(token string, wg *sync.WaitGroup) {
	defer wg.Done()
	defer func() {
		if err := recover(); err != nil {
			log.Sugar.Error(err)
		}
	}()
	client := http_client.GetHttpClient()
	startTime := carbon.Now().AddHours(-24).TimestampWithMillisecond()
	endTime := carbon.Now().TimestampWithMillisecond()
	resp, err := client.R().SetQueryParams(map[string]string{
		"sort":            "-timestamp",
		"limit":           "50",
		"start":           "0",
		"direction":       "2",
		"db_version":      "1",
		"trc20Id":         "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
		"address":         token,
		"start_timestamp": stdutil.ToString(startTime),
		"end_timestamp":   stdutil.ToString(endTime),
	}).Get(UsdtTrc20ApiUri)
	if err != nil {
		panic(err)
	}
	if resp.StatusCode() != http.StatusOK {
		panic(err)
	}
	var trc20Resp UsdtTrc20Resp
	err = json.Cjson.Unmarshal(resp.Body(), &trc20Resp)
	if err != nil {
		panic(err)
	}
	if trc20Resp.PageSize <= 0 {
		return
	}
	for _, transfer := range trc20Resp.Data {
		if transfer.To != token || transfer.ContractRet != "SUCCESS" {
			continue
		}
		decimalQuant, err := decimal.NewFromString(transfer.Amount)
		if err != nil {
			panic(err)
		}
		decimalDivisor := decimal.NewFromFloat(1000000)
		amount := decimalQuant.Div(decimalDivisor).InexactFloat64()
		tradeId, err := data.GetTradeIdByWalletAddressAndAmount(token, amount)
		if err != nil {
			panic(err)
		}
		if tradeId == "" {
			continue
		}
		order, err := data.GetOrderInfoByTradeId(tradeId)
		if err != nil {
			panic(err)
		}
		// 区块的确认时间必须在订单创建时间之后
		createTime := order.CreatedAt.TimestampWithMillisecond()
		if transfer.BlockTimestamp < createTime {
			panic("Orders cannot actually be matched")
		}
		// 到这一步就完全算是支付成功了
		req := &request.OrderProcessingRequest{
			Token:              token,
			TradeId:            tradeId,
			Amount:             amount,
			BlockTransactionId: transfer.Hash,
		}
		err = OrderProcessing(req)
		if err != nil {
			panic(err)
		}
		// 回调队列
		orderCallbackQueue, _ := handle.NewOrderCallbackQueue(order)
		mq.MClient.Enqueue(orderCallbackQueue, asynq.MaxRetry(5))
		// 发送机器人消息
		msgTpl := `
<b>📢📢有新的交易支付成功！</b>
<pre>交易号：%s</pre>
<pre>订单号：%s</pre>
<pre>请求支付金额：%f cny</pre>
<pre>实际支付金额：%f usdt</pre>
<pre>钱包地址：%s</pre>
<pre>订单创建时间：%s</pre>
<pre>支付成功时间：%s</pre>
`
		msg := fmt.Sprintf(msgTpl, order.TradeId, order.OrderId, order.Amount, order.ActualAmount, order.Token, order.CreatedAt.ToDateTimeString(), carbon.Now().ToDateTimeString())
		telegram.SendToBot(msg)
	}
}

// SolanaCallBack solana usdt callback
func SolanaCallBack(token string, wg *sync.WaitGroup) {
	defer wg.Done()
	defer func() {
		if err := recover(); err != nil {
			log.Sugar.Error(err)
		}
	}()

	walletPK, err := solana.PublicKeyFromBase58(token)
	if err != nil {
		// not a valid solana address
		return
	}
	mintPK := solana.MustPublicKeyFromBase58(UsdtSolanaMint)
	ata, _, err := solana.FindAssociatedTokenAddress(walletPK, mintPK)
	if err != nil {
		log.Sugar.Error(err)
		return
	}

	client := http_client.GetHttpClient()
	sigBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "getSignaturesForAddress",
		"params":  []interface{}{ata.String(), map[string]int{"limit": 20}},
	}
	resp, err := client.R().SetHeader("Content-Type", "application/json").SetBody(sigBody).Post(SolanaRpcUri)
	if err != nil {
		panic(err)
	}
	var sigResp SignaturesForAddressResp
	err = json.Cjson.Unmarshal(resp.Body(), &sigResp)
	if err != nil {
		panic(err)
	}
	for _, sig := range sigResp.Result {
		txBody := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "getTransaction",
			"params": []interface{}{sig.Signature, map[string]interface{}{
				"encoding":                       "json",
				"maxSupportedTransactionVersion": 0,
			}},
		}
		txRespRaw, err := client.R().SetHeader("Content-Type", "application/json").SetBody(txBody).Post(SolanaRpcUri)
		if err != nil {
			panic(err)
		}
		var txResp SolanaTransactionResp
		err = json.Cjson.Unmarshal(txRespRaw.Body(), &txResp)
		if err != nil {
			panic(err)
		}
		if txResp.Result.Meta.Err != nil {
			continue
		}
		preAmt := decimal.Zero
		if amt, ok := findTokenAmount(txResp.Result.Meta.PreTokenBalances, token); ok {
			preAmt = amt
		}
		postAmt, ok := findTokenAmount(txResp.Result.Meta.PostTokenBalances, token)
		if !ok {
			continue
		}
		diff := postAmt.Sub(preAmt)
		if diff.Cmp(decimal.Zero) <= 0 {
			continue
		}
		amount := diff.InexactFloat64()
		tradeId, err := data.GetTradeIdByWalletAddressAndAmount(token, amount)
		if err != nil {
			panic(err)
		}
		if tradeId == "" {
			continue
		}
		order, err := data.GetOrderInfoByTradeId(tradeId)
		if err != nil {
			panic(err)
		}
		createTime := order.CreatedAt.Timestamp()
		if sig.BlockTime*1000 < createTime {
			continue
		}
		req := &request.OrderProcessingRequest{
			Token:              token,
			TradeId:            tradeId,
			Amount:             amount,
			BlockTransactionId: sig.Signature,
		}
		err = OrderProcessing(req)
		if err != nil {
			panic(err)
		}
		orderCallbackQueue, _ := handle.NewOrderCallbackQueue(order)
		mq.MClient.Enqueue(orderCallbackQueue, asynq.MaxRetry(5))
		msgTpl := `
<b>📢📢有新的交易支付成功！</b>
<pre>交易号：%s</pre>
<pre>订单号：%s</pre>
<pre>请求支付金额：%f cny</pre>
<pre>实际支付金额：%f usdt</pre>
<pre>钱包地址：%s</pre>
<pre>订单创建时间：%s</pre>
<pre>支付成功时间：%s</pre>
`
		msg := fmt.Sprintf(msgTpl, order.TradeId, order.OrderId, order.Amount, order.ActualAmount, order.Token, order.CreatedAt.ToDateTimeString(), carbon.Now().ToDateTimeString())
		telegram.SendToBot(msg)
	}
}

func findTokenAmount(balances []TokenBalance, owner string) (decimal.Decimal, bool) {
	for _, b := range balances {
		if b.Owner == owner && b.Mint == UsdtSolanaMint {
			amt, err := decimal.NewFromString(b.UiTokenAmount.Amount)
			if err != nil {
				return decimal.Zero, false
			}
			factor := decimal.NewFromInt(1).Shift(int32(b.UiTokenAmount.Decimals))
			return amt.Div(factor), true
		}
	}
	return decimal.Zero, false
}
