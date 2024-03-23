package main

import (
	"context"
	sdkmath "cosmossdk.io/math"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/gogo/protobuf/proto"
	"github.com/joho/godotenv"
	github_com_tendermint_tendermint_libs_bytes "github.com/tendermint/tendermint/libs/bytes"
	rpchttp "github.com/tendermint/tendermint/rpc/client/http"
	libclient "github.com/tendermint/tendermint/rpc/jsonrpc/client"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

type Coin struct {
	Amount sdkmath.Int `json:"amount"`
	Denom  string      `json:"denom"`
}

var wg sync.WaitGroup

func main() {
	// Load the .env file

	err := godotenv.Load()
	if err != nil {
		fmt.Println("Error loading .env file")
	}
	rest := os.Getenv("REST_ENDPOINT")
	rpc := os.Getenv("RPC_ENDPOINT")
	contract := os.Getenv("REDBANK_CONTRACT")

	// Get all addresses
	redBankUsers, _, err := getAllAddresses(contract, rpc)
	if err != nil {
		fmt.Println("Error:", err)
		return
	}

	for _, address := range redBankUsers {
		fmt.Println("user address: ", address)

		debtCoins := []Coin{}
		collateralCoins := []Coin{}

		// Create a wait group
		var wg sync.WaitGroup
		wg.Add(3)

		// Launch goroutines for debt and collateral positions
		go func() {
			defer wg.Done()
			debtCoins, err = userPosition(rest, contract, "user_debts", address)
			if err != nil {
				fmt.Println("Error:", err)
				return
			}
		}()

		go func() {
			defer wg.Done()
			collateralCoins, err = userPosition(rest, contract, "user_collaterals", address)
			if err != nil {
				fmt.Println("Error:", err)
				return
			}
		}()

		go func() {
			defer wg.Done()
			err = userTotalPosition(rest, contract, "user_position_liquidation_pricing", address)
			if err != nil {
				fmt.Println("Error:", err)
				return
			}
		}()

		wg.Wait()

		fmt.Println("debtCoins: ", debtCoins, "collateralCoins: ", collateralCoins)

		collateralizationRatio := calculateCollateralizationRatio(debtCoins, collateralCoins)

		if len(debtCoins) == 0 {
			fmt.Println("Collateralization Ratio N/A as zero debt")
		} else {
			fmt.Println("Collateralization Ratio: ", collateralizationRatio)
		}
		fmt.Println("---------------------------------------------------")
	}

}

func calculateCollateralizationRatio(debtCoins, collateralCoins []Coin) sdkmath.LegacyDec {
	// considering the decimal places as 10^6 as of now, will be changed to the actual decimal places
	// once we get the actual decimal places from the api
	// calculate the total debt
	totalDebt := sdkmath.NewInt(0)
	for _, debt := range debtCoins {
		// get price of the debt coin
		price := getPrice(debt.Denom)
		totalDebt = totalDebt.Add(debt.Amount.Mul(price))
	}

	// calculate the total collateral
	totalCollateral := sdkmath.NewInt(0)
	for _, collateral := range collateralCoins {
		price := getPrice(collateral.Denom)
		totalCollateral = totalCollateral.Add(collateral.Amount.Mul(price))
	}

	if totalDebt.IsZero() {
		return sdkmath.LegacyNewDec(0)
	}
	collateralizationRatio := sdkmath.LegacyNewDecFromInt(totalCollateral).Quo(sdkmath.LegacyNewDecFromInt(totalDebt))

	return collateralizationRatio.Quo(sdkmath.LegacyNewDec(100))

}

func getPrice(denom string) sdkmath.Int {
	// get the price of the denom
	// for now, we will return 1 as the price, but this will come from an oracle
	return sdkmath.NewInt(1)
}

func userTotalPosition(rest, contract, queryType, address string) error {

	query := fmt.Sprintf(`{"%s": {"user": "%s"}}`, queryType, address)

	base64Encode := base64.StdEncoding.EncodeToString([]byte(query))

	url := rest + "/cosmwasm/wasm/v1/contract/" + contract + "/smart/" + base64Encode

	response, err := http.Get(url)
	if err != nil {
		fmt.Println("Error:", err)
		return err
	}
	defer response.Body.Close()

	var body map[string]interface{}

	err = json.NewDecoder(response.Body).Decode(&body)
	if err != nil {
		return err
	}

	if item, ok := body["data"].(interface{}); ok {
		if dataItem, ok := item.(map[string]interface{}); ok {
			health_status := dataItem["health_status"]
			if !ok {
				fmt.Println("Error: Unable to get health status")
				return nil
			}
			total_collateralized_debt, ok := dataItem["total_collateralized_debt"].(string)
			if !ok {
				return nil
			}
			total_enabled_collateral, ok := dataItem["total_enabled_collateral"].(string)
			if !ok {
				return nil
			}
			fmt.Println("Health Status: ", health_status)
			fmt.Println("Total Collateralized Debt: ", total_collateralized_debt)
			fmt.Println("Total Enabled Collateral: ", total_enabled_collateral)
		}

	}
	return nil

}

func userPosition(rest, contract, queryType, address string) ([]Coin, error) {

	coin := []Coin{}

	query := fmt.Sprintf(`{"%s": {"user": "%s"}}`, queryType, address)

	base64Encode := base64.StdEncoding.EncodeToString([]byte(query))

	url := rest + "/cosmwasm/wasm/v1/contract/" + contract + "/smart/" + base64Encode

	response, err := http.Get(url)
	if err != nil {
		fmt.Println("Error:", err)
		return nil, err
	}
	defer response.Body.Close()

	var body map[string]interface{}

	err = json.NewDecoder(response.Body).Decode(&body)
	if err != nil {
		return nil, err
	}

	if data, ok := body["data"].([]interface{}); ok {
		for _, item := range data {
			if dataItem, ok := item.(map[string]interface{}); ok {
				amountStr, ok := dataItem["amount"].(string)
				if !ok {
					continue
				}
				denomStr, ok := dataItem["denom"].(string)
				if !ok {
					continue
				}
				amount, ok := sdkmath.NewIntFromString(amountStr)
				if !ok {
					continue
				}
				coin = append(coin, Coin{Denom: denomStr, Amount: amount})
			}
		}
	}
	return coin, nil

}

func getAllAddresses(contract, rpc string) ([]string, int64, error) {

	results := []string{}
	var totalScanned int64

	client, err := NewRPCClient(rpc, time.Second*30)
	if err != nil {
		return results, totalScanned, err
	}

	var stateRequest QueryAllContractStateRequest
	stateRequest.Address = contract

	rpcRequest, err := proto.Marshal(&stateRequest)
	if err != nil {
		return results, totalScanned, err
	}

	rpcResponse, err := client.ABCIQuery(
		context.Background(),
		"/cosmwasm.wasm.v1.Query/AllContractState",
		rpcRequest,
	)
	if err != nil {
		return results, totalScanned, err
	}

	var stateResponse QueryAllContractStateResponse
	err = proto.Unmarshal(rpcResponse.Response.GetValue(), &stateResponse)
	if err != nil {
		return results, totalScanned, err
	}

	accounts := make(map[string]HealthCheckWorkItem)

	for _, model := range stateResponse.Models {

		totalScanned++

		hexKey := model.Key
		if len(hexKey) < 50 {
			// Anything shorter than 50 can't be a map
			continue
		}

		lengthIndicator := hexKey[0:2]
		length, err := strconv.ParseInt(lengthIndicator.String(), 16, 64)
		if err != nil {
			fmt.Println("Error: Unable to decode contract state key (map name) ", err)
			continue
		}
		// Shift to next section
		hexKey = hexKey[2:]
		// Shift to next section
		hexKey = hexKey[length:]

		// Determine the length of the address
		lengthIndicator = hexKey[0:2]
		length, err = strconv.ParseInt(lengthIndicator.String(), 16, 64)
		if err != nil {
			fmt.Println("Error: Unable to decode contract state key (address) ", err)
			continue
		}
		// Shift to next section
		hexKey = hexKey[2:]
		// Address is next
		address := hexKey[0:length]
		// Shift to next section
		hexKey = hexKey[length:]

		identifier := string(address)

		if _, ok := accounts[identifier]; !ok {
			// Not added yet, init
			accounts[identifier] = HealthCheckWorkItem{
				Identifier: identifier,
			}
		}
	}

	for _, workItem := range accounts {
		// Unmarshal the JSON string into the struct
		err := json.Unmarshal([]byte(workItem.Identifier), &data)
		if err != nil {
			fmt.Println("Error:", err)
			return results, totalScanned, err
		}
		results = append(results, data.Addr)

	}
	// fmt.Println("Addresses:", results)

	return results, totalScanned, nil

}

type HealthCheckWorkItem struct {
	Identifier string `json:"identifier"`
}

var data struct {
	Addr string `json:"addr"`
}

func NewRPCClient(addr string, timeout time.Duration) (*rpchttp.HTTP, error) {
	httpClient, err := libclient.DefaultHTTPClient(addr)
	if err != nil {
		return nil, err
	}
	httpClient.Timeout = timeout
	rpcClient, err := rpchttp.NewWithClient(addr, httpClient)
	if err != nil {
		return nil, err
	}
	return rpcClient, nil
}

const _ = proto.GoGoProtoPackageIsVersion3 // please upgrade the proto package

// QueryAllContractStateRequest is the request type for the
// Query/AllContractState RPC method
type QueryAllContractStateRequest struct {
	// address is the address of the contract
	Address string `protobuf:"bytes,1,opt,name=address,proto3" json:"address,omitempty"`
}

func (m *QueryAllContractStateRequest) Reset()         { *m = QueryAllContractStateRequest{} }
func (m *QueryAllContractStateRequest) String() string { return proto.CompactTextString(m) }
func (*QueryAllContractStateRequest) ProtoMessage()    {}

// QueryAllContractStateResponse is the response type for the
// Query/AllContractState RPC method
type QueryAllContractStateResponse struct {
	Models []Model `protobuf:"bytes,1,rep,name=models,proto3" json:"models"`
}

func (m *QueryAllContractStateResponse) Reset()         { *m = QueryAllContractStateResponse{} }
func (m *QueryAllContractStateResponse) String() string { return proto.CompactTextString(m) }
func (*QueryAllContractStateResponse) ProtoMessage()    {}

type Model struct {
	// hex-encode key to read it better (this is often ascii)
	Key github_com_tendermint_tendermint_libs_bytes.HexBytes `protobuf:"bytes,1,opt,name=key,proto3,casttype=github.com/tendermint/tendermint/libs/bytes.HexBytes" json:"key,omitempty"`
	// base64-encode raw value
	Value []byte `protobuf:"bytes,2,opt,name=value,proto3" json:"value,omitempty"`
}

func (m *Model) Reset()         { *m = Model{} }
func (m *Model) String() string { return proto.CompactTextString(m) }
func (*Model) ProtoMessage()    {}

type PageRequest struct {
	// key is a value returned in PageResponse.next_key to begin
	// querying the next page most efficiently. Only one of offset or key
	// should be set.
	Key []byte `protobuf:"bytes,1,opt,name=key,proto3" json:"key,omitempty"`
	// offset is a numeric offset that can be used when key is unavailable.
	// It is less efficient than using key. Only one of offset or key should
	// be set.
	Offset uint64 `protobuf:"varint,2,opt,name=offset,proto3" json:"offset,omitempty"`
	// limit is the total number of results to be returned in the result page.
	// If left empty it will default to a value to be set by each app.
	Limit uint64 `protobuf:"varint,3,opt,name=limit,proto3" json:"limit,omitempty"`
	// count_total is set to true  to indicate that the result set should include
	// a count of the total number of items available for pagination in UIs.
	// count_total is only respected when offset is used. It is ignored when key
	// is set.
	CountTotal bool `protobuf:"varint,4,opt,name=count_total,json=countTotal,proto3" json:"count_total,omitempty"`
	// reverse is set to true if results are to be returned in the descending order.
	//
	// Since: cosmos-sdk 0.43
	Reverse bool `protobuf:"varint,5,opt,name=reverse,proto3" json:"reverse,omitempty"`
}

func (m *PageRequest) Reset()         { *m = PageRequest{} }
func (m *PageRequest) String() string { return proto.CompactTextString(m) }
func (*PageRequest) ProtoMessage()    {}

func (m *PageRequest) GetKey() []byte {
	if m != nil {
		return m.Key
	}
	return nil
}

func (m *PageRequest) GetOffset() uint64 {
	if m != nil {
		return m.Offset
	}
	return 0
}

func (m *PageRequest) GetLimit() uint64 {
	if m != nil {
		return m.Limit
	}
	return 0
}

func (m *PageRequest) GetCountTotal() bool {
	if m != nil {
		return m.CountTotal
	}
	return false
}

func (m *PageRequest) GetReverse() bool {
	if m != nil {
		return m.Reverse
	}
	return false
}

type PageResponse struct {
	// next_key is the key to be passed to PageRequest.key to
	// query the next page most efficiently
	NextKey []byte `protobuf:"bytes,1,opt,name=next_key,json=nextKey,proto3" json:"next_key,omitempty"`
	// total is total number of results available if PageRequest.count_total
	// was set, its value is undefined otherwise
	Total uint64 `protobuf:"varint,2,opt,name=total,proto3" json:"total,omitempty"`
}

func (m *PageResponse) Reset()         { *m = PageResponse{} }
func (m *PageResponse) String() string { return proto.CompactTextString(m) }
func (*PageResponse) ProtoMessage()    {}

func (m *PageResponse) GetNextKey() []byte {
	if m != nil {
		return m.NextKey
	}
	return nil
}

func (m *PageResponse) GetTotal() uint64 {
	if m != nil {
		return m.Total
	}
	return 0
}
