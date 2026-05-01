package main

import (
	"context"
	"log"

	"github.com/aws/aws-lambda-go/lambda"
	"put-event-poc/internal/order"
)

func handler(ctx context.Context, detail order.Detail) (order.Detail, error) {
	log.Printf("[Job1: order confirmation] orderId=%s userId=%s amount=%d",
		detail.OrderID, detail.UserID, detail.Amount)
	return detail, nil
}

func main() {
	lambda.Start(handler)
}
