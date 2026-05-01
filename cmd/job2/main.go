package main

import (
	"context"
	"log"

	"github.com/aws/aws-lambda-go/lambda"
	"put-event-poc/internal/order"
)

func handler(ctx context.Context, detail order.Detail) (order.Detail, error) {
	log.Printf("[Job2: inventory allocation] orderId=%s items=%d",
		detail.OrderID, len(detail.Items))
	return detail, nil
}

func main() {
	lambda.Start(handler)
}
