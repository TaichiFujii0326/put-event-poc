package main

import (
	"context"
	"encoding/json"
	"log"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
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

func handler(ctx context.Context, ebEvent events.EventBridgeEvent) error {
	log.Printf("source: %s", ebEvent.Source)
	log.Printf("detail-type: %s", ebEvent.DetailType)
	log.Printf("detail (raw): %s", string(ebEvent.Detail))

	var detail OrderDetail
	if err := json.Unmarshal(ebEvent.Detail, &detail); err != nil {
		log.Printf("failed to unmarshal order detail: %v", err)
		return err
	}

	log.Printf("orderId: %s, userId: %s, amount: %d",
		detail.OrderID, detail.UserID, detail.Amount)
	return nil
}

func main() {
	lambda.Start(handler)
}
