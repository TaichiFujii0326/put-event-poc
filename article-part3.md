---
title: "EventBridge + Step Functions で直列ジョブを実装した（Go + SAM）"
emoji: "🔄"
type: "tech"
topics: ["aws", "eventbridge", "stepfunctions", "sqs", "lambda", "sam", "go"]
published: false
---

## はじめに

[前回の記事](https://github.com/TaichiFujii0326/put-event-poc) では EventBridge → SQS → Lambda + DLQ の構成で、イベント駆動アーキテクチャの基本を実装しました。

今回はその構成に **AWS Step Functions** を組み合わせて、**直列ジョブの実行保証**を実装します。

| | Part 1 | Part 2 | Part 3（今回）|
|---|---|---|---|
| 構成 | EventBridge → Lambda | EventBridge → SQS → Lambda | EventBridge → SQS → Lambda → Step Functions |
| 直列実行の保証 | ❌ | ❌ | ✅ |
| 失敗時のリトライ | ❌ | ✅（SQS） | ✅（SQS + Step Functions） |
| 失敗したジョブの把握 | できない | DLQで確認 | Step Functions のコンソールで確認 |

---

## なぜ Step Functions が必要なのか

EventBridge 単体では**処理の順序を保証しません**。「Job1 が完了してから Job2 を実行したい」という要件には対応できません。

Step Functions を使うと、ジョブの実行順序・リトライ・エラーハンドリングを**ステートマシン**として定義できます。

```
EventBridge → SQS → Lambda（起動役） → Step Functions
                                           → Job1（注文確認）
                                           → Job2（在庫引き当て）※ Job1 完了後
```

Lambda は「Step Functions を起動するだけ」に専念し、ジョブの制御は Step Functions に任せます。

---

## プロジェクト構成

```
put-event-poc/
├── template-sfn.yaml          # SAM テンプレート（Step Functions 構成）
├── internal/
│   └── order/
│       └── types.go           # 共通の型定義
├── cmd/
│   ├── put-event/
│   │   └── main.go            # イベント送信スクリプト（既存）
│   ├── sfn-starter/
│   │   └── main.go            # SQS を受けて Step Functions を起動する Lambda
│   ├── job1/
│   │   └── main.go            # Job1: 注文確認
│   └── job2/
│       └── main.go            # Job2: 在庫引き当て
```

---

## 共通型の切り出し

Job1・Job2・sfn-starter の3ファイルで同じ `OrderDetail` 型を使います。重複を避けるため `internal/order` パッケージに切り出します。

```go
// internal/order/types.go
package order

type Detail struct {
	OrderID string     `json:"orderId"`
	UserID  string     `json:"userId"`
	Amount  int        `json:"amount"`
	Items   []LineItem `json:"items"`
}

type LineItem struct {
	ProductID string `json:"productId"`
	Quantity  int    `json:"quantity"`
}
```

---

## sfn-starter Lambda

SQS からイベントを受け取り、Step Functions の実行を開始するだけの Lambda です。

```go
// cmd/sfn-starter/main.go
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
```

### ポイント①：SDK クライアントは `init()` で初期化する

Lambda の Execution Environment はウォームスタート時に再利用されます。`handler` 内で毎回 `config.LoadDefaultConfig` を呼ぶと、呼び出しごとに HTTP コネクションが張り直されて無駄が生じます。`init()` で一度だけ初期化することで、ウォームスタート時のレイテンシを下げられます。

### ポイント②：Partial Batch Response でメッセージを個別に処理する

```go
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
```

ループ内で `return err` すると、バッチ全体が SQS に戻されて成功済みのレコードも再処理されます。`BatchItemFailures` に失敗したレコードの ID だけを返すことで、失敗したものだけをリトライ対象にできます。

### ポイント③：`ExecutionAlreadyExists` を無視する

```go
out, err := sfnClient.StartExecution(ctx, &sfn.StartExecutionInput{
	StateMachineArn: aws.String(stateMachineArn),
	Name:            aws.String(detail.OrderID),  // 実行名 = OrderID
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
```

Step Functions では、`Name` が同じ実行が 90 日以内に存在すると `ExecutionAlreadyExists` エラーになります。SQS のリトライと組み合わさると、再試行のたびに必ず失敗し続けて DLQ に入ってしまいます。冪等性を保つために、このエラーは明示的に無視します。

---

## Job Lambda

Job1・Job2 は `order.Detail` を受け取ってログを出すだけです。Step Functions が出力を次のステートへ渡すため、受け取った値をそのまま返します。

```go
// cmd/job1/main.go
func handler(ctx context.Context, detail order.Detail) (order.Detail, error) {
	log.Printf("[Job1: order confirmation] orderId=%s userId=%s amount=%d",
		detail.OrderID, detail.UserID, detail.Amount)
	return detail, nil
}
```

```go
// cmd/job2/main.go
func handler(ctx context.Context, detail order.Detail) (order.Detail, error) {
	log.Printf("[Job2: inventory allocation] orderId=%s items=%d",
		detail.OrderID, len(detail.Items))
	return detail, nil
}
```

---

## SAM テンプレート（抜粋）

### ステートマシンの定義

```yaml
OrderStateMachine:
  Type: AWS::StepFunctions::StateMachine
  Properties:
    StateMachineName: poc-order-state-machine
    RoleArn: !GetAtt StateMachineRole.Arn
    DefinitionString: !Sub |
      {
        "Comment": "Order processing workflow",
        "StartAt": "Job1_OrderConfirmation",
        "States": {
          "Job1_OrderConfirmation": {
            "Type": "Task",
            "Resource": "${Job1Function.Arn}",
            "Retry": [
              {
                "ErrorEquals": ["Lambda.ServiceException", "Lambda.TooManyRequestsException"],
                "IntervalSeconds": 2,
                "MaxAttempts": 3,
                "BackoffRate": 2
              }
            ],
            "Catch": [{ "ErrorEquals": ["States.ALL"], "Next": "JobFailed" }],
            "Next": "Job2_InventoryAllocation"
          },
          "Job2_InventoryAllocation": {
            "Type": "Task",
            "Resource": "${Job2Function.Arn}",
            "Retry": [...],
            "Catch": [{ "ErrorEquals": ["States.ALL"], "Next": "JobFailed" }],
            "End": true
          },
          "JobFailed": {
            "Type": "Fail",
            "Error": "JobFailed",
            "Cause": "A job in the workflow failed"
          }
        }
      }
```

`Retry` で Lambda のスロットリングや一時的なエラーを自動リトライし、上限を超えたら `Catch` で `JobFailed` ステートに遷移させます。

### Partial Batch Response を有効にする

```yaml
Events:
  SQSTrigger:
    Type: SQS
    Properties:
      Queue: !GetAtt EventQueueSfn.Arn
      BatchSize: 1
      FunctionResponseTypes:
        - ReportBatchItemFailures  # ← これが必要
```

Lambda 側で `batchItemFailures` を返しても、SQS トリガー側でこの設定がないと有効になりません。

---

## デプロイ手順

### 1. ビルド

```bash
sam build --template template-sfn.yaml
```

### 2. デプロイ

:::message alert
**`sam deploy --template template-sfn.yaml` はやってはいけない**

`--template` にソーステンプレートを渡すと、コンパイル済みバイナリではなくソースコードがデプロイされます。`provided.al2023` ランタイムは `bootstrap` バイナリを必要とするため、`Runtime.InvalidEntrypoint` エラーになります。

`sam build` 後は `.aws-sam/build/template.yaml`（ビルド済みテンプレート）を使います。
:::

```bash
sam deploy \
  --template .aws-sam/build/template.yaml \
  --stack-name put-event-poc-sfn \
  --resolve-s3 \
  --capabilities CAPABILITY_IAM \
  --region ap-northeast-1
```

### 3. イベント送信

```bash
# cmd/put-event/main.go の eventBusName を poc-event-bus-sfn に変更してから
make put-event
```

### 4. Step Functions コンソールで確認

AWS コンソールで **Step Functions → ステートマシン → poc-order-state-machine** を開きます。実行が `SUCCEEDED` になっていれば成功です。

CloudWatch Logs でも確認できます：

```
# Job1
[Job1: order confirmation] orderId=order-001 userId=user-abc amount=3000

# Job2
[Job2: inventory allocation] orderId=order-001 items=2
```

---

## まとめ

今回実装した構成のポイントを振り返ります。

| ポイント | 内容 |
|---|---|
| SDK クライアントは `init()` で初期化 | ウォームスタート時の接続コストを削減 |
| Partial Batch Response | 失敗したレコードだけ SQS に戻す |
| `ExecutionAlreadyExists` を無視 | リトライ時の冪等性を保証 |
| `Retry` / `Catch` の定義 | Step Functions レベルでのエラーハンドリング |
| `sam deploy` は build 済みテンプレートを使う | `Runtime.InvalidEntrypoint` を回避 |

EventBridge はルーティング、SQS はバッファ、Step Functions は順序制御と、それぞれの責務を明確に分けることで、スケールしやすい構成になります。

## ソースコード

https://github.com/TaichiFujii0326/put-event-poc
