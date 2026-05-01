package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	sfntypes "github.com/aws/aws-sdk-go-v2/service/sfn/types"
	"put-event-poc/internal/order"
)

var sfnClient *sfn.Client

func init() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	sfnClient = sfn.NewFromConfig(cfg)
}

type batchItemFailure struct {
	ItemIdentifier string `json:"itemIdentifier"`
}

type batchResponse struct {
	BatchItemFailures []batchItemFailure `json:"batchItemFailures"`
}

func handler(ctx context.Context, sqsEvent events.SQSEvent) (batchResponse, error) {
	stateMachineArn := os.Getenv("STATE_MACHINE_ARN")
	if stateMachineArn == "" {
		return batchResponse{}, fmt.Errorf("STATE_MACHINE_ARN is not set")
	}

	var failures []batchItemFailure

	for _, record := range sqsEvent.Records {
		if err := processRecord(ctx, record, stateMachineArn); err != nil {
			log.Printf("failed to process record %s: %v", record.MessageId, err)
			failures = append(failures, batchItemFailure{ItemIdentifier: record.MessageId})
		}
	}

	return batchResponse{BatchItemFailures: failures}, nil
}

func processRecord(ctx context.Context, record events.SQSMessage, stateMachineArn string) error {
	var ebEvent events.CloudWatchEvent
	if err := json.Unmarshal([]byte(record.Body), &ebEvent); err != nil {
		return fmt.Errorf("failed to unmarshal EventBridge event: %w", err)
	}

	var detail order.Detail
	if err := json.Unmarshal(ebEvent.Detail, &detail); err != nil {
		return fmt.Errorf("failed to unmarshal order detail: %w", err)
	}

	inputJSON, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("failed to marshal input: %w", err)
	}

	out, err := sfnClient.StartExecution(ctx, &sfn.StartExecutionInput{
		StateMachineArn: aws.String(stateMachineArn),
		Name:            aws.String(detail.OrderID),
		Input:           aws.String(string(inputJSON)),
	})
	if err != nil {
		var ae *sfntypes.ExecutionAlreadyExists
		if errors.As(err, &ae) {
			log.Printf("execution already exists for orderId=%s, skipping", detail.OrderID)
			return nil
		}
		return fmt.Errorf("failed to start execution: %w", err)
	}

	log.Printf("started execution: %s", *out.ExecutionArn)
	return nil
}

func main() {
	lambda.Start(handler)
}
