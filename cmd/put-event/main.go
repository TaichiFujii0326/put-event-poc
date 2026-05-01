package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
)

const (
	eventBusName = "poc-event-bus"
	region       = "ap-northeast-1"
)

type OrderDetail struct {
	OrderID string     `json:"orderId"`
	UserID  string     `json:"userId"`
	Amount  int        `json:"amount"`
	Items   []LineItem `json:"items"`
}

type LineItem struct {
	ProductID string `json:"productId"`
	Quantity  int    `json:"quantity"`
}

func main() {
	ctx := context.Background()

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	client := eventbridge.NewFromConfig(cfg)

	detail := OrderDetail{
		OrderID: "order-001",
		UserID:  "user-abc",
		Amount:  3000,
		Items: []LineItem{
			{ProductID: "prod-1", Quantity: 2},
			{ProductID: "prod-2", Quantity: 1},
		},
	}

	// Detail は JSON 文字列として渡す必要がある
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		log.Fatalf("failed to marshal detail: %v", err)
	}

	input := &eventbridge.PutEventsInput{
		Entries: []types.PutEventsRequestEntry{
			{
				EventBusName: aws.String(eventBusName),
				Source:       aws.String("poc.order"),
				DetailType:   aws.String("OrderPlaced"),
				Detail:       aws.String(string(detailJSON)),
			},
		},
	}

	resp, err := client.PutEvents(ctx, input)
	if err != nil {
		log.Fatalf("PutEvents error: %v", err)
	}

	if resp.FailedEntryCount > 0 {
		for _, entry := range resp.Entries {
			if entry.ErrorCode != nil {
				log.Printf("failed entry: %s - %s", *entry.ErrorCode, *entry.ErrorMessage)
			}
		}
		log.Fatal("some entries failed")
	}

	if len(resp.Entries) > 0 && resp.Entries[0].EventId != nil {
		fmt.Printf("Successfully sent %d event(s). EventID: %s\n",
			len(input.Entries), *resp.Entries[0].EventId)
	}
}
