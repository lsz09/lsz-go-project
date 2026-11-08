# CloudQueue

Go와 AWS를 활용한 비동기 파일 처리 및 작업 관리 플랫폼입니다.

사용자가 파일 처리 작업을 요청하면 API 서버가 작업을 메시지 큐에 전달하고,
Worker가 비동기로 처리한 후 결과와 작업 상태를 저장합니다.

## 현재 구현 상태

- [x] Go 프로젝트 초기화
- [x] API 서버 실행
- [x] Health Check API
- [x] PostgreSQL 연결 및 Readiness API
- [x] Database Migration 기반 구축
- [x] `jobs` 테이블 스키마
- [x] GitHub Actions CI
- [x] 작업 생성 API
- [x] 작업 목록 조회 API
- [x] 작업 단건 조회 API
- [x] Job 상태 전이 Repository 및 Service
- [x] Worker 작업 처리 오케스트레이션
- [x] 웹 서버 로그 분석 코어
- [x] 로컬 파일 기반 로그 Processor
- [x] 로컬 one-shot Worker 실행 프로그램
- [x] Job 메시지 계약 및 메시지 큐 인터페이스
- [x] LocalStack 기반 SQS Publisher 및 Consumer
- [x] Job 생성 API SQS 메시지 발행 연동
- [x] 메시지 큐 기반 비동기 Worker
- [x] 메시지 큐
- [x] 로컬 로그 파일 업로드 및 Job 생성
- [x] LocalStack 기반 S3 Storage 및 API·Worker 연동
- [ ] AWS 배포
- [ ] Observability

## 로컬 실행

### 환경변수 설정

프로젝트 루트에서 예시 환경변수 파일을 복사합니다.

```cmd
copy .env.example .env
```

기본 로컬 PostgreSQL 호스트 포트는 `5433`입니다. 실제 비밀번호와 접속 정보는
Git에서 제외된 `.env`에만 저장하고 `.env.example`에는 예시 값만 유지합니다.

### PostgreSQL 실행

```cmd
docker compose up -d postgres
```

상태 확인:

```cmd
docker compose ps
```

### LocalStack SQS 실행

실제 AWS 계정과 비용 없이 SQS 연동을 개발할 수 있도록 LocalStack을 사용합니다.
PostgreSQL과 LocalStack을 함께 실행합니다.

```powershell
docker compose up -d
```

LocalStack이 준비되면 `cloudqueue-jobs` Queue가 자동으로 생성됩니다. 서비스 상태와
Queue 목록은 다음 명령으로 확인합니다.

```powershell
docker compose ps
docker compose exec localstack awslocal sqs list-queues
```

### API 실행

```cmd
go run ./cmd/api
```

## Database Migration

데이터베이스 스키마는
[golang-migrate](https://github.com/golang-migrate/migrate)를 사용해 관리합니다.
Migration 파일은 프로젝트 루트의 `migrations` 디렉터리에 있습니다.

### Migration CLI 설치

```powershell
go install -tags postgres github.com/golang-migrate/migrate/v4/cmd/migrate@v4.19.1
```

`migrate` 명령을 찾을 수 없다면 현재 PowerShell 세션의 `PATH`에 Go 실행 파일
디렉터리를 추가합니다.

```powershell
$env:Path += ";$(go env GOPATH)\bin"
```

### 환경변수 로딩

Migration CLI는 `.env`를 자동으로 읽지 않으므로 현재 PowerShell 세션에
환경변수를 로딩합니다.

```powershell
Get-Content .env | ForEach-Object {
    if ($_ -match '^\s*([^#][^=]*)=(.*)$') {
        $name = $matches[1].Trim()
        $value = $matches[2].Trim()

        Set-Item -Path "Env:$name" -Value $value
    }
}
```

### Migration 적용

```powershell
migrate -path migrations -database $env:DATABASE_URL up
```

### 현재 Migration 버전 확인

```powershell
migrate -path migrations -database $env:DATABASE_URL version
```

### 최근 Migration 한 단계 롤백

```powershell
migrate -path migrations -database $env:DATABASE_URL down 1
```

### 롤백 후 재적용

```powershell
migrate -path migrations -database $env:DATABASE_URL up
```

## Database Schema

현재 `jobs` 테이블은 다음 작업 상태를 지원합니다.

- `PENDING`
- `PROCESSING`
- `COMPLETED`
- `FAILED`

주요 컬럼:

| 컬럼 | 설명 |
| --- | --- |
| `id` | UUID 작업 식별자 |
| `status` | 작업 상태 |
| `file_name` | 원본 파일 이름 |
| `file_key` | 입력 파일 저장소 키 |
| `result_key` | 결과 파일 저장소 키 |
| `error_message` | 실패 원인 |
| `created_at` | 생성 시각 |
| `updated_at` | 수정 시각 |
| `started_at` | 처리 시작 시각 |
| `completed_at` | 처리 완료 시각 |

## Job API

### 작업 생성

`file_name`은 필수이며 `file_key`는 선택값입니다.

```http
POST /api/v1/jobs
Content-Type: application/json
```

요청 예시:

```json
{
  "file_name": "access.log",
  "file_key": null
}
```

성공 시 PostgreSQL에 `PENDING` 작업을 저장하고, 생성된 Job ID를 SQS에 발행한 뒤
`201 Created`와 작업 정보를 반환합니다.

```json
{
  "id": "생성된 UUID",
  "status": "PENDING",
  "file_name": "access.log",
  "file_key": null,
  "created_at": "2026-09-13T12:00:00Z"
}
```

잘못된 JSON이나 입력에는 `400 Bad Request`, 지원하지 않는 Method에는
`405 Method Not Allowed`, 내부 오류에는 상세를 숨긴 `500 Internal Server Error`를
반환합니다.

```text
POST /api/v1/jobs
    ↓
PostgreSQL에 PENDING Job 저장
    ↓
{"job_id":"생성된 UUID"} 메시지를 SQS에 발행
    ↓
201 Created
```

Job 저장에 실패하면 메시지를 발행하지 않습니다. SQS 발행에 실패하면 내부 상세를 숨긴
`500 Internal Server Error`를 반환합니다. 현재는 PostgreSQL 저장과 SQS 발행을 하나의
트랜잭션으로 묶지 않으므로, 발행 실패 시 이미 생성된 `PENDING` Job이 남을 수 있습니다.
이 일관성 보완은 Transactional Outbox 또는 복구 작업을 도입하는 후속 작업 범위입니다.

### 작업 목록 조회

작업은 생성 시각 내림차순으로 조회하며, 생성 시각이 같으면 UUID 내림차순으로
정렬합니다. `limit`의 기본값은 `20`, 최댓값은 `100`이고 `offset`의 기본값은
`0`입니다. `limit=0`도 기본값 `20`으로 처리합니다.

```http
GET /api/v1/jobs?limit=20&offset=0
```

성공 시 `200 OK`와 작업의 전체 상태 필드 및 실제 적용된 페이지 옵션을 반환합니다.

```json
{
  "jobs": [
    {
      "id": "작업 UUID",
      "status": "PENDING",
      "file_name": "access.log",
      "file_key": null,
      "result_key": null,
      "error_message": null,
      "created_at": "2026-09-13T12:00:00Z",
      "updated_at": "2026-09-13T12:00:00Z",
      "started_at": null,
      "completed_at": null
    }
  ],
  "limit": 20,
  "offset": 0
}
```

조회 결과가 없으면 `jobs`는 `null`이 아닌 빈 배열 `[]`입니다. 음수·범위 초과·
정수가 아닌 페이지 값, 중복 쿼리, 알 수 없는 쿼리는 `400 Bad Request`를 반환합니다.

### 작업 단건 조회

작업 UUID로 현재 상태와 전체 상세 정보를 조회합니다.

```http
GET /api/v1/jobs/00000000-0000-0000-0000-000000000001
```

성공 시 `200 OK`와 작업 정보를 반환합니다.

```json
{
  "id": "00000000-0000-0000-0000-000000000001",
  "status": "PENDING",
  "file_name": "access.log",
  "file_key": null,
  "result_key": null,
  "error_message": null,
  "created_at": "2026-09-14T12:00:00Z",
  "updated_at": "2026-09-14T12:00:00Z",
  "started_at": null,
  "completed_at": null
}
```

잘못된 UUID에는 `400 Bad Request`, 존재하지 않는 작업에는 `404 Not Found`를
반환합니다. 단건 경로에서 GET 이외의 Method에는 `405 Method Not Allowed`와
`Allow: GET` 헤더를 반환하며, 내부 오류 상세정보는 응답에 노출하지 않습니다.

## 상태 확인 API

API 프로세스 상태:

```http
GET /health
```

PostgreSQL을 포함한 요청 처리 준비 상태:

```http
GET /ready
```

PostgreSQL 연결 성공 시 `/ready`는 `200 OK`, 연결 실패 시
`503 Service Unavailable`을 반환합니다. PostgreSQL 연결 상태와 관계없이
API 프로세스가 실행 중이면 `/health`는 `200 OK`를 반환합니다.

## Job Repository

`internal/job`은 기존 `pgxpool.Pool`을 `job.NewRepository(pool)`로 전달받아
`Create`, `FindByID`, `List`를 제공하며 Service 계층을 통해 Job API에 연결됩니다.

- `Create(ctx, job.CreateParams{FileName: "access.log"})`는 `PENDING` 작업을
  생성하고 DB의 UUID와 생성 시각을 포함한 `Job`을 반환합니다.
- `FindByID(ctx, id)`는 UUID로 조회합니다. `errors.Is`로 `job.ErrNotFound`와
  `job.ErrInvalidInput`을 구분할 수 있고, DB 오류는 원인을 보존해 반환합니다.
- `List(ctx, job.ListOptions{Limit: 20, Offset: 0})`는 생성 시각 내림차순,
  동일 시각에는 UUID 내림차순으로 조회합니다. limit 0은 기본값 20이며,
  음수·100 초과 limit 및 음수 offset은 거부합니다. 빈 결과는 비어 있는
  non-nil 슬라이스입니다. 동시 삽입 시 offset 페이지 경계는 바뀔 수 있습니다.
- nullable 컬럼은 포인터로 NULL과 빈 값을 구분합니다. 빈 파일명 및 공백만
  있는 파일명은 거부하지만, 유효한 파일명은 입력 그대로 저장합니다.
- DB 호출은 최대 5초이며, 호출자의 더 짧은 deadline과 취소를 유지합니다.

### Job 상태 전이

Worker가 사용할 상태 변경은 Repository와 Service에서 다음 규칙으로 제한합니다.

```text
PENDING → PROCESSING
PROCESSING → COMPLETED
PROCESSING → FAILED
```

| 기능 | 갱신 내용 |
| --- | --- |
| `MarkProcessing` | `status`, `started_at`, `updated_at` |
| `MarkCompleted` | `status`, `result_key`, `completed_at`, `updated_at` |
| `MarkFailed` | `status`, `error_message`, `completed_at`, `updated_at` |

상태 변경 SQL은 현재 상태를 `WHERE` 조건으로 확인하는 원자적 UPDATE입니다. 여러
Worker가 같은 `PENDING` 작업을 동시에 획득해도 하나만 `PROCESSING` 변경에 성공하고,
나머지는 `ErrInvalidTransition`을 반환합니다. 존재하지 않는 작업은 `ErrNotFound`,
잘못된 UUID와 빈 결과 키 또는 실패 메시지는 `ErrInvalidInput`으로 구분합니다.

`COMPLETED`와 `FAILED`는 최종 상태이므로 다른 상태로 변경할 수 없습니다. 모든 상태
변경은 DB 시각으로 `updated_at`을 갱신하고 기존 Context 및 최대 5초 timeout을
유지합니다.

## Worker 작업 처리 오케스트레이션

`internal/worker`의 `Worker`는 작업 ID를 받아 상태 전이와 실제 처리 순서를 조정합니다.
실제 파일 처리 방식은 `Processor` 인터페이스로 분리되어 있어 이후 메시지 큐와 파일
분석 기능을 연결할 수 있습니다.

```text
MarkProcessing → Processor.Process → MarkCompleted
                              └─ 오류 → MarkFailed
```

- `MarkProcessing`에 실패한 작업은 Processor를 실행하지 않습니다.
- Processor가 반환한 결과 저장소 키는 변경하지 않고 `MarkCompleted`에 전달합니다.
- Processor 오류는 메시지를 `MarkFailed`에 저장한 뒤 호출자에게 그대로 추적 가능하게
  반환합니다. 실패 상태 저장도 함께 실패하면 `errors.Join`으로 두 오류를 모두 보존합니다.
- 호출자의 Context는 모든 단계에 동일하게 전달되며, Processor 실행 전에 취소되면
  처리를 시작하지 않습니다.
- `QueueRunner`는 SQS 메시지를 반복 수신하고 처리 성공 후에만 삭제합니다. 처리 실패
  메시지는 삭제하지 않고 다음 메시지 polling을 계속합니다.

## 웹 서버 로그 분석

`internal/loganalysis`는 외부 인프라에 의존하지 않고 `io.Reader`로 전달된 Nginx 및
Apache Combined Log Format을 한 줄씩 분석합니다.

```text
127.0.0.1 - - [16/Sep/2026:10:00:00 +0900] "GET /api/v1/jobs HTTP/1.1" 200 512 "-" "Mozilla/5.0"
```

분석 결과에는 전체 요청 수, 오류 요청 수와 오류율, HTTP 상태 코드·Method·endpoint별
요청 수가 포함됩니다. 400 이상인 상태 코드를 오류 요청으로 집계하고, query string이
다른 요청도 동일한 URL path로 합산합니다.

빈 입력은 초기화된 빈 통계를 반환합니다. 잘못된 로그는 조용히 건너뛰지 않고 줄 번호와
`ErrInvalidLogLine` 원인을 포함한 오류를 반환하며, Context가 취소되면 다음 줄의 분석을
시작하지 않습니다. 분석 코어는 파일 저장소에 직접 의존하지 않으며 아래의 로컬
Processor가 파일 입출력과 Worker 인터페이스 연결을 담당합니다.

## 로컬 파일 기반 로그 Processor

`internal/logprocessor`는 Worker의 `Processor` 인터페이스를 구현하며 로컬 Storage에서
로그를 읽어 기존 `loganalysis.Analyzer`로 분석한 뒤 JSON 결과를 저장합니다.

```text
PROCESSING Job
    ↓
uploads/{job-id}/access.log 읽기
    ↓
웹 서버 로그 분석
    ↓
results/{job-id}.json 저장
    ↓
결과 키 반환
```

로컬 Storage root 아래의 디렉터리 구조는 다음과 같습니다.

```text
data/
├── uploads/
│   └── {job-id}/
│       └── access.log
└── results/
    └── {job-id}.json
```

Storage key는 `/`를 사용하는 상대 경로만 허용합니다. 절대 경로, Windows 전용 경로,
NUL 문자 및 `..`를 통해 root 밖으로 이동하는 경로는 `ErrInvalidInput`으로 거부합니다.
결과는 대상 디렉터리의 임시 파일에 먼저 기록하고 `Sync`와 `Close`가 성공한 후 최종
경로로 rename합니다. 실패하면 완성되지 않은 결과 파일을 남기지 않고 임시 파일을
정리합니다.

Processor는 Job 상태를 직접 변경하지 않습니다. Worker가 상태 전이를 담당하며,
Processor는 성공 시 `results/{job-id}.json` 키만 반환합니다. 로컬 파일과 S3 Storage를
같은 인터페이스로 지원하며 저장 방식은 실행 환경변수로 선택합니다.

## 로컬 Worker 실행

API 서버와 Worker는 각각 `cmd/api`, `cmd/worker`의 독립 프로세스로 실행합니다.
Worker는 `-job-id`를 전달하면 작업 하나만 처리하고 종료하며, 옵션을 생략하면 SQS를
long polling하는 장기 실행 모드로 동작합니다. 단건 처리 성공 시 종료 코드 `0`,
설정·DB 연결·작업 처리 실패 시 non-zero를 반환합니다.

`.env`에는 PostgreSQL 접속 정보와 로컬 Storage root를 설정합니다. 실제 비밀번호는
`.env.example`이 아니라 Git에서 제외된 `.env`에만 저장합니다.

```dotenv
DATABASE_URL=postgres://cloudqueue:실제비밀번호@localhost:5433/cloudqueue?sslmode=disable
LOCAL_STORAGE_ROOT=./data
```

입력 로그 파일은 Storage root 아래에 준비합니다. `data/`는 Git에서 제외됩니다.

```text
data/
├── uploads/
│   └── access.log
└── results/
    └── {job-id}.json
```

예시 입력 로그:

```text
127.0.0.1 - - [16/Sep/2026:10:00:00 +0900] "GET /health HTTP/1.1" 200 42 "-" "Mozilla/5.0"
127.0.0.1 - - [16/Sep/2026:10:01:00 +0900] "POST /api/v1/jobs HTTP/1.1" 500 128 "-" "Mozilla/5.0"
```

API 서버를 실행한 뒤 입력 파일의 Storage key가 포함된 작업을 생성합니다.

```http
POST /api/v1/jobs
Content-Type: application/json
```

```json
{
  "file_name": "access.log",
  "file_key": "uploads/access.log"
}
```

응답의 Job ID를 Worker에 전달합니다.

```powershell
go run ./cmd/worker -job-id {job-id}
```

SQS에 발행된 작업을 계속 처리하려면 `-job-id` 없이 실행합니다. 이 모드에는 아래
LocalStack SQS Adapter 절의 AWS 및 SQS 환경변수가 모두 필요합니다.

```powershell
go run ./cmd/worker
```

정상 처리 후 작업은 `COMPLETED`, `result_key`는 `results/{job-id}.json`이 되며
같은 위치에 분석 결과 JSON 파일이 생성됩니다. 입력 파일이 없거나 로그 형식이
잘못되면 작업은 `FAILED`가 되고 `error_message`에 실패 원인이 저장됩니다.
큐 모드는 처리 성공 메시지만 삭제하고, 처리 실패 메시지는 삭제하지 않아 visibility
timeout 이후 다시 전달될 수 있게 합니다. 빈 polling 결과는 정상 상태로 간주합니다.
`Ctrl+C` 또는 `SIGTERM`을 받으면 실행 중인 수신·작업 Context를 취소하고 정상 종료합니다.

## Job 메시지 및 Queue 계약

`internal/jobqueue`는 API와 Worker가 메시지 큐를 통해 Job ID를 전달할 때 사용할
인프라 독립적인 계약을 제공합니다. 메시지에는 Job ID만 포함하고, 상태와 파일 정보는
Worker가 PostgreSQL에서 다시 조회합니다.

```json
{
  "job_id": "00000000-0000-0000-0000-000000000001"
}
```

메시지의 Job ID는 Repository와 동일한 `job.ValidateID` 규칙으로 검증합니다. 빈 값,
공백이 포함된 값, 하이픈이 없는 값 및 UUID가 아닌 값은 `job.ErrInvalidInput`과
`jobqueue.ErrInvalidMessage`로 구분할 수 있습니다. JSON 디코딩은 알 수 없는 필드,
두 개 이상의 JSON 값 및 객체가 아닌 입력을 거부하며 오류에 메시지 원문을 노출하지
않습니다.

메시지 큐 연동 경계는 다음 인터페이스로 분리되어 있습니다.

```text
Publisher.Publish → 메시지 발행
Consumer.Receive  → Delivery 수신
Worker.ProcessJob → Job 처리
Consumer.Delete   → 성공한 메시지만 삭제
```

`Delivery`의 `ReceiptHandle`은 큐 구현이 발급하는 불투명한 확인 토큰입니다. 메시지를
수신한 즉시 삭제하지 않고 Worker 처리가 성공한 뒤에만 삭제합니다. 처리에 실패하면
삭제하지 않아 향후 큐의 재전달 정책을 적용할 수 있습니다.

Message, Delivery, Publisher 및 Consumer 계약을 기반으로 LocalStack SQS Adapter까지
구현되어 있습니다. Job 생성 API는 Publisher를 사용해 메시지를 발행하며, Worker의
`QueueRunner`가 Consumer를 통해 지속적으로 수신합니다.

## LocalStack SQS Adapter

`internal/jobqueue/sqsqueue`는 AWS SDK for Go v2를 사용해 기존 Publisher와 Consumer
계약을 SQS로 구현합니다. 공통 `jobqueue` 패키지에는 AWS SDK 타입이 노출되지 않습니다.

LocalStack 연결에는 다음 환경변수를 사용합니다.

```dotenv
AWS_REGION=ap-northeast-2
AWS_ACCESS_KEY_ID=test
AWS_SECRET_ACCESS_KEY=test
SQS_ENDPOINT_URL=http://localhost:4566
SQS_QUEUE_URL=http://localhost.localstack.cloud:4566/queue/ap-northeast-2/000000000000/cloudqueue-jobs
SQS_WAIT_TIME_SECONDS=10
```

`SQS_WAIT_TIME_SECONDS`는 0초에서 20초까지 허용합니다. Publisher는 기존
`jobqueue.Encode`로 생성한 JSON을 `SendMessage`로 발행하고, Consumer는 한 번에 최대
한 개의 메시지를 long polling으로 받아 `jobqueue.Decode`로 검증합니다. 메시지가
없으면 `jobqueue.ErrNoMessage`를 반환합니다.

```text
Publisher.Publish
    ↓
SQS SendMessage
    ↓
Consumer.Receive
    ↓
Worker 처리 성공
    ↓
Consumer.Delete
```

Consumer는 메시지를 수신할 때 자동으로 삭제하지 않습니다. 처리에 성공한 뒤 전달받은
`ReceiptHandle`로 `Delete`를 호출해야 하며, 처리에 실패하면 삭제하지 않아 SQS가
메시지를 다시 전달할 수 있게 합니다.

API 서버는 시작할 때 환경변수로 SQS Client와 Publisher를 구성합니다. Job 생성 흐름은
Job Service와 Publisher를 조합하는 Submission Service를 거쳐 PostgreSQL 저장 후 SQS에
메시지를 발행합니다. Worker도 같은 환경변수로 SQS Client와 Consumer를 구성하며,
메시지 body나 `ReceiptHandle`, 자격 증명은 로그에 기록하지 않습니다.

## SQS 재전달 제한 및 DLQ

LocalStack 초기화 스크립트는 원본 `cloudqueue-jobs`와 실패 메시지 보관용
`cloudqueue-jobs-dlq`를 구성합니다. 기본 설정은 다음과 같습니다.

```dotenv
SQS_QUEUE_NAME=cloudqueue-jobs
SQS_DLQ_NAME=cloudqueue-jobs-dlq
SQS_MAX_RECEIVE_COUNT=5
```

원본 Queue의 Visibility Timeout은 30초, DLQ의 메시지 보존 기간은 14일입니다.
수신 후 삭제되지 않은 메시지는 Visibility Timeout 이후 다시 전달될 수 있고,
Redrive Policy의 최대 수신 횟수를 초과하면 SQS가 DLQ로 이동합니다. 이는 Job의
실행 횟수가 아니라 메시지의 수신 횟수 기준입니다. 콘솔이나 CLI의 수신도 횟수에
영향을 주므로 운영 Queue 메시지를 확인할 때 주의해야 합니다.

`SQS_MAX_RECEIVE_COUNT`는 1~1000의 정수로 설정하며 선행 0은 허용하지 않습니다.
Queue 이름은 80자 이하의 영문·숫자·하이픈·밑줄을 허용하며, 원본과 DLQ 이름은
달라야 합니다. 명시적으로 빈 DLQ 이름이나 수신 횟수를 전달하면 초기화에 실패합니다.

초기화는 기존 Queue를 조회하고 속성만 갱신하므로 Queue와 기존 메시지를 삭제하지
않습니다. `.env`의 값을 변경했다면 LocalStack 컨테이너를 재구성하고 초기화 스크립트를
재실행합니다. 이 스크립트는 LocalStack 개발용이며 실제 AWS 리소스는 변경하지 않습니다.

```powershell
docker compose up -d localstack
docker compose exec localstack sh /etc/localstack/init/ready.d/init-aws.sh
docker compose exec localstack awslocal sqs list-queues
```

### Worker 메시지 정책

| 상태 또는 오류 | 동작 |
| --- | --- |
| PENDING | 원자적 상태 전이로 획득 후 처리, 성공하면 메시지 삭제 |
| COMPLETED | DB 상태를 확인하고 재분석 없이 중복 메시지 삭제 |
| FAILED·PROCESSING·미존재 Job | 메시지 미삭제, 다음 polling 계속 |
| DB 조회·상태 전이 오류 | 메시지 미삭제, 다음 polling 계속 |
| 잘못된 JSON·Job ID·알 수 없는 필드·ReceiptHandle 누락 | 메시지 미삭제, 안전한 경고 후 다음 polling 계속 |
| SQS 수신·삭제 요청 오류 | 안전한 오류를 반환하고 Worker 종료 |
| Context 취소 | 추가 수신·삭제 없이 정상 종료 |

`QueueProcessor`는 큐 실행에만 적용됩니다. 기존 `-job-id` 단건 실행의 상태 전이
검증은 유지됩니다. 동시 Worker의 상태 획득 충돌은 성공으로 간주하지 않습니다.
메시지 본문과 ReceiptHandle은 로그에 기록하지 않습니다.

FAILED는 최종 상태이므로 메시지가 재전달되어도 Job을 재실행하지 않습니다.
DLQ 자동 복원, 실패 Job 자동 재실행 및 고아 PROCESSING 복구는 아직 지원하지 않습니다.
처리 중 Visibility Timeout은 아래 heartbeat 설정으로 자동 연장합니다.

### Queue 설정 및 DLQ 확인

다음 예시는 기본 Queue 이름을 사용합니다. 이름을 바꿨다면 함께 수정합니다.

```powershell
$sourceQueueUrl = docker compose exec -T localstack awslocal sqs get-queue-url --queue-name cloudqueue-jobs --query QueueUrl --output text
docker compose exec -T localstack awslocal sqs get-queue-attributes --queue-url $sourceQueueUrl --attribute-names RedrivePolicy VisibilityTimeout
$deadLetterQueueUrl = docker compose exec -T localstack awslocal sqs get-queue-url --queue-name cloudqueue-jobs-dlq --query QueueUrl --output text
docker compose exec -T localstack awslocal sqs get-queue-attributes --queue-url $deadLetterQueueUrl --attribute-names ApproximateNumberOfMessages MessageRetentionPeriod
docker compose exec -T localstack awslocal sqs receive-message --queue-url $deadLetterQueueUrl --max-number-of-messages 1 --wait-time-seconds 1
```

마지막 명령은 DLQ 메시지를 실제로 수신하며 visibility에 영향을 줍니다. 자동으로
삭제하거나 원본 Queue로 복원하지는 않습니다.

### 테스트

```powershell
go test ./internal/worker ./internal/jobqueue/...
```

전용 PostgreSQL과 LocalStack을 실행하고 아래 통합 테스트 절의 환경변수를 설정합니다.
테스트는 고유한 원본 Queue와 DLQ, 1초 Visibility Timeout, 최대 수신 횟수 2를 사용합니다.

```powershell
go test -tags=integration -v ./cmd/worker -run TestQueueWorkerRedriveIntegration
```

잘못된 메시지와 FAILED Job 메시지의 DLQ 이동, 정상 Job 처리 지속, 완료된 Job의
중복 메시지 삭제 및 결과 파일 불변을 검증합니다. CI는 전체 integration race 검사와
초기화 스크립트의 재실행·설정 갱신·기존 메시지 보존·잘못된 설정 거부도 확인합니다.

정책 참고: [AWS SQS DLQ 문서](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-dead-letter-queues.html)

## 처리 중 Visibility Timeout 자동 연장

Queue Worker는 유효한 메시지를 수신하면 Job 처리 전에 해당 메시지의 Visibility
Timeout을 설정하고, 처리 중 heartbeat로 계속 갱신합니다. 원본 Queue의 기본 30초
설정과 달리 개별 Delivery에만 적용되며, 메시지가 재수신되면 Queue 기본값으로 돌아갑니다.

```dotenv
SQS_VISIBILITY_TIMEOUT_SECONDS=60
SQS_VISIBILITY_HEARTBEAT_SECONDS=20
```

설정을 생략하면 메시지를 60초 동안 숨기고 20초 간격으로 갱신합니다. 명시적으로
빈 값, 정수가 아닌 값, 0 및 음수는 거부합니다. timeout은 1~43200초 범위에서
검증하며, 실제 환경변수 조합은 heartbeat와 요청 예산에 대한 여유도 만족해야 합니다.
연장 API 호출의 제한 시간은 5초로 고정하고, **heartbeat + 2 × 요청 제한 시간 <
visibility timeout**인 설정만 허용합니다. 예를 들어 timeout 30초·heartbeat 20초는
여유 부족으로 거부합니다. 이 조건은 네트워크나 스케줄러 지연에 대한 완전한 보장은 아닙니다.

`SQS_WAIT_TIME_SECONDS`는 빈 Queue의 Receive long polling 대기 시간이며 heartbeat와
별개입니다. 기존 `-job-id` one-shot 모드에서는 새 환경변수를 읽지 않습니다.

```text
유효한 Delivery 수신
→ 최초 Visibility Timeout 설정
→ QueueProcessor 실행 + heartbeat
→ 처리 종료
→ heartbeat 중단·진행 중인 API 취소·goroutine 종료 확인
→ 처리 성공·연장 오류 없음·Context 유효인 경우에만 Delete
```

최초 연장 실패 시 Job을 시작하지 않고, 메시지를 남긴 채 Worker가 오류 종료합니다.
처리 중 연장이 실패하거나 연장 API 요청이 시간 제한을 초과하면 처리 Context를
취소하고, 처리와 heartbeat의 종료를 확인한 뒤 메시지를 남기고 오류 종료합니다.
연장 요청은 SDK 재시도도 1회 시도로 제한하며 애플리케이션 수준의 재시도는 하지 않습니다.

Job 처리 자체가 실패하면 heartbeat를 중단하고 다음 메시지를 처리합니다. 이미
COMPLETED인 Job은 재분석 없이 삭제하며 FAILED·PROCESSING·미존재 Job의 메시지는
삭제하지 않습니다. 잘못된 메시지는 연장하지 않고 기존 DLQ 정책을 따릅니다.

Ctrl+C, SIGINT 및 SIGTERM은 처리와 진행 중인 연장을 취소합니다. 정상 종료를 위한
취소만 발생했다면 장애로 기록하지 않습니다. 처리 종료와 실제 연장 오류가 겹쳐도
연장 오류를 확인한 후 삭제 여부를 결정하며, 처리 오류가 함께 발생하면 원인도 보존합니다.
Processor와 외부 Adapter는 Context 취소를 관찰해야 정상 종료할 수 있습니다.

### 제한 사항

- SQS는 수신 시점부터 최대 12시간의 visibility 제한을 적용하므로 heartbeat로 무기한
  연장할 수 없습니다. 남은 허용 시간보다 긴 연장을 요청하면 SQS가 오류를 반환하고
  Worker는 위 연장 실패 정책을 따릅니다. 최대값 43200초도 실제 남은 시간에 따라 실패할 수 있습니다.
- 연장은 중복 전달 가능성을 낮추지만 exactly-once 처리를 보장하지 않습니다.
  기존 DB 상태 조회와 원자적 PENDING 획득을 유지합니다.
- 취소 시 Job이 PROCESSING으로 남거나, 완료 직후 연장 오류로 COMPLETED 메시지가
  남을 수 있습니다. 자동 복구는 포함하지 않으며 완료된 Job의 재전달은 기존 중복 처리 정책으로 처리합니다.
- 메시지를 삭제하지 않을 때 숨김 시간을 0으로 되돌리지 않습니다. 마지막으로 성공한
  연장의 시간이 지나면 메시지가 다시 보일 수 있습니다.

### 테스트

```powershell
go test ./internal/worker ./internal/jobqueue/sqsqueue ./cmd/worker
```

단위 테스트는 가상 시간(`testing/synctest`)과 채널로 연장 주기, API timeout, 처리
완료와 연장 실패의 경쟁 상황, 정상 종료 및 goroutine 종료를 검증합니다.

전용 PostgreSQL·LocalStack을 실행하고 통합 테스트 환경변수를 설정한 뒤 실행합니다.

```powershell
go test -tags=integration -v ./cmd/worker -run TestVisibilityHeartbeatIntegration
```

Queue 기본 visibility 1초, 최초 연장 2초, heartbeat 250ms를 사용해 실제 로그 처리를
3초 이상 유지합니다. 이 동안 경쟁 Consumer가 메시지를 받지 못하는지 확인하고,
성공 시 결과 파일 생성·메시지 삭제, 실패 시 연장 중단·메시지 재전달을 검증합니다.
검증 중 다른 Consumer가 메시지를 받으면 수신 횟수에 영향을 준다는 점도 고려합니다.

참고: [AWS ChangeMessageVisibility 문서](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ChangeMessageVisibility.html)

## LocalStack S3 Storage

API와 Worker는 공통 `internal/storage` 설정 및 Factory로 저장소를 구성합니다.
`STORAGE_BACKEND` 생략 시 기존 로컬 모드가 적용되고, 명시적으로 빈 값이나
지원하지 않는 backend는 시작 단계에서 거부합니다.

로컬 모드:

```dotenv
STORAGE_BACKEND=local
LOCAL_STORAGE_ROOT=./data
```

S3 모드:

```dotenv
STORAGE_BACKEND=s3
S3_ENDPOINT_URL=http://localhost:4566
S3_BUCKET_NAME=cloudqueue-files
S3_USE_PATH_STYLE=true
AWS_REGION=ap-northeast-2
AWS_ACCESS_KEY_ID=test
AWS_SECRET_ACCESS_KEY=test
```

local 모드에서는 S3 설정을 요구하지 않고, s3 모드에서는 `LOCAL_STORAGE_ROOT`를
요구하지 않습니다. S3 endpoint는 인증정보·query·경로가 없는 HTTP(S) origin으로
설정합니다. `S3_USE_PATH_STYLE` 생략 시 LocalStack용 true를 사용하며 잘못된 boolean은
거부합니다. API와 Worker는 동일한 backend·버킷·region을 사용해야 합니다.

Compose의 LocalStack은 `sqs,s3` 서비스를 함께 실행합니다. `init-s3.sh`는 인증된
버킷 목록 조회가 성공한 경우에만 기존 버킷 유무를 판단하고, 버킷이 없을 때 생성합니다.
재실행해도 기존 객체를 삭제하지 않습니다. API와 Worker는 버킷을 자동 생성하지 않습니다.

```powershell
docker compose up -d
docker compose exec localstack sh /etc/localstack/init/ready.d/init-s3.sh
docker compose exec localstack awslocal s3api list-buckets
```

DB Migration 적용 후 API와 Worker를 각각 실행하고 파일을 업로드합니다.

```powershell
go run ./cmd/api
```

다른 터미널에서:

```powershell
go run ./cmd/worker
```

파일 업로드:

```powershell
curl.exe -X POST http://localhost:8080/api/v1/jobs -F "file=@access.log"
```

기존 JSON Job 생성과 one-shot 실행도 같은 backend를 사용합니다.

```powershell
go run ./cmd/worker -job-id {job-id}
```

### 객체 구조와 구현

```text
cloudqueue-files/
├── uploads/{upload-id}/access.log
└── results/{job-id}.json
```

Job에는 버킷 URL이 아니라 기존 `file_key`와 `result_key`만 저장합니다. `internal/s3storage`가
GetObject·PutObject·DeleteObject를 구현하고 AWS SDK 타입은 공통 Storage 계약에 노출하지
않습니다. 로컬과 S3가 같은 key 검증을 사용하며 절대경로·역슬래시·드라이브명·NUL 및
`..` 경로 구간을 거부합니다. 업로드 원본 파일명 검증과 10 MiB/11 MiB 제한도 유지됩니다.

Open은 응답 Body를 스트리밍 Reader로 반환하며 호출자가 닫습니다. Put은 임의의
Reader를 32 KiB 버퍼로 권한이 제한된 임시 파일에 복사한 뒤, 길이를 아는 seekable
Body로 단일 PutObject를 호출합니다. SDK의 서명·길이 계산을 지원하면서 전체 객체를
메모리에 추가로 복사하지 않기 위한 선택입니다. 입력 Reader는 닫지 않으며 임시 파일은
성공·실패 모두 닫고 삭제합니다. multipart S3 업로드는 사용하지 않습니다.

따라서 s3 모드에서도 쓰기 가능한 임시 디스크 공간이 필요합니다. HTTP multipart 파싱의
임시 파일과 S3 업로드 준비용 파일이 동시에 존재할 수 있습니다. Context 취소는 SDK와
입력 복사에 전달되고, 오류는 원인을 보존하면서 응답·로그·Job 오류에 안전한 단계만 표시합니다.

### 실패 및 객체 정리

| 상황 | 동작 |
| --- | --- |
| 입력 저장 실패 | Job 미생성, 해당 요청 입력 객체 정리 시도 |
| Job 미생성이 확실한 실패 | 입력 객체 정리 |
| DB 저장 결과 불확실·SQS 발행 실패 | 입력 객체 보존 |
| 입력 읽기·로그 분석·결과 저장 실패 | 기존 Worker FAILED 처리, 메시지 미삭제 |
| 결과 저장 후 DB 완료 기록 실패 | 결과 객체 보존, 메시지 미삭제 |

정리는 해당 요청이 만든 고유한 key만 대상으로 합니다. 버킷 전체나 prefix를 삭제하지
않으며, 원래 오류와 정리 실패를 함께 보존합니다. 성공 Job의 입력·결과 객체는 유지합니다.
저장 결과가 불확실한 오류나 프로세스 강제 종료 시 객체·임시 파일이 남을 수 있고,
고아 객체 자동 정리는 아직 지원하지 않습니다. SQS heartbeat·DLQ·중복 메시지 정책은 유지됩니다.

### 객체 확인

기본 버킷 이름을 변경했다면 아래 명령도 함께 수정합니다.

```powershell
docker compose exec localstack awslocal s3api list-objects-v2 --bucket cloudqueue-files
docker compose exec localstack awslocal s3 cp s3://cloudqueue-files/results/{job-id}.json -
```

로컬 파일은 S3로 자동 이전되지 않습니다. backend를 변경하면 기존 Job의 key가 다른
저장소를 가리키므로, 기존 Job을 처리하려면 해당 입력 객체를 먼저 이전해야 합니다.
Job별 backend 구분은 지원하지 않습니다. 현재 Compose는 LocalStack 데이터 영속화를
구성하지 않으므로 컨테이너 재생성 시 객체가 사라질 수 있습니다. 실제 AWS 배포와
IAM 구성은 이번 구현에 포함되지 않습니다.

### 테스트

```powershell
go test ./internal/s3storage ./internal/storage ./internal/logprocessor ./cmd/worker
```

전용 PostgreSQL과 SQS·S3가 활성화된 LocalStack을 실행한 뒤 설정합니다.

```powershell
$env:TEST_DATABASE_URL = "postgres://cloudqueue_test:${testPassword}@localhost:55433/cloudqueue_test?sslmode=disable"
$env:TEST_SQS_ENDPOINT_URL = "http://localhost:4566"
$env:TEST_S3_ENDPOINT_URL = "http://localhost:4566"
$env:TEST_AWS_REGION = "ap-northeast-2"
go test -tags=integration -v ./cmd/worker -run 'TestS3UploadedJobIntegration|TestS3OneShotCommandIntegration'
Remove-Item Env:TEST_DATABASE_URL, Env:TEST_SQS_ENDPOINT_URL, Env:TEST_S3_ENDPOINT_URL, Env:TEST_AWS_REGION
```

테스트는 고유한 버킷·객체·Queue·DB Schema만 생성·정리합니다. 실제 multipart 업로드부터
S3 입력 확인·SQS 수신·heartbeat Worker·S3 결과 통계·메시지 삭제를 검증하며, 잘못된 로그,
입력 누락, 결과 저장 실패, 발행 실패 시 객체 보존 및 실제 one-shot 명령 조립도 확인합니다.
CI는 S3를 활성화한 전체 integration race 검사와 초기화 스크립트 검증을 수행합니다.

SDK 참고: [AWS SDK for Go v2 S3 입력 Body 문서](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/sdk-utilities-s3.html)

## 로그 파일 업로드

`POST /api/v1/jobs`는 기존 JSON 요청과 함께 `multipart/form-data` 파일 업로드를
지원합니다. local 모드에서는 API와 Worker에 동일한 `LOCAL_STORAGE_ROOT`를 설정해야 합니다.
s3 모드에서는 위의 S3 Storage 절에 따라 동일한 버킷을 설정합니다.

PostgreSQL과 LocalStack을 실행하고 Migration을 적용한 뒤 API와 Worker를 각각 실행합니다.

```powershell
go run ./cmd/api
```

다른 터미널에서 Worker를 시작합니다.

```powershell
go run ./cmd/worker
```

로그 파일을 업로드합니다. multipart boundary는 curl이 생성하므로 Content-Type을
수동으로 지정하지 않습니다.

```powershell
curl.exe -X POST http://localhost:8080/api/v1/jobs -F "file=@access.log"
```

파일은 `uploads/{upload-id}/access.log`에 저장되며, 원본 파일명은 Job의 `file_name`에
기록합니다. upload-id는 Job ID와 별도로 서버가 생성한 UUID입니다. 응답은 기존 생성
API와 동일한 `201 Created` JSON이며 `id`, `status`, `file_name`, `file_key`, `created_at`을
포함합니다. 응답의 `id`로 `GET /api/v1/jobs/{id}`를 호출해 진행 상태를 확인할 수 있습니다.
Worker는 비동기로 처리하므로 조회 시 이미 상태가 변경되었을 수 있습니다.

파일은 `file` 필드 한 개만 허용하고 최대 크기는 **10 MiB**입니다. 전체 multipart 요청은
**11 MiB**로 제한합니다. Content-Length가 없는 요청에도 실제 읽은 크기로 제한을
적용합니다. 파일 데이터는 최대 64 KiB만 메모리에 유지하고 나머지는 임시 파일로
처리하며 요청 종료 시 정리합니다. 로그 내용의 형식 검사는 Worker에서 수행하므로
잘못된 로그도 업로드는 성공한 뒤 Job이 `FAILED`가 될 수 있습니다.

| 상황 | 응답 |
| --- | --- |
| 파일 누락·빈 파일·여러 파일·추가 필드·위험한 파일명·잘못된 multipart | `400 Bad Request` |
| 파일 또는 전체 요청 크기 제한 초과 | `413 Request Entity Too Large` |
| 지원하지 않는 Content-Type | `415 Unsupported Media Type` |
| Storage·DB·SQS 처리 실패 | `500 Internal Server Error` |

절대경로, 경로 구분자, `..`, 제어문자가 있는 원본 파일명은 거부하며, 원본 이름을
저장 경로로 사용하지 않습니다. 원본 파일명은 UTF-8 기준 255바이트까지 허용합니다.
기존 JSON 클라이언트의 호환성을 위해 Content-Type을 생략한 요청은 JSON으로 처리합니다.

파일 저장 실패와 Job 생성 전 취소는 해당 파일을 정리합니다. DB가 Job을 생성하지
않았음을 확실히 알 수 있는 오류도 입력 파일을 정리합니다. DB 저장 여부가 불확실하거나
Job 생성 후 SQS 발행이 실패하면 파일과 Job을 보존합니다. 이때 HTTP 오류 뒤 재요청하면
중복 Job이 생길 수 있으며, Outbox·고아 파일 복구·요청 멱등성은 아직 지원하지 않습니다.
성공한 Job의 입력 파일은 자동 삭제하지 않습니다. 파일 정리 시 빈 상위 디렉터리는 유지합니다.

업로드 단위 테스트는 다음과 같이 실행합니다.

```powershell
go test ./internal/jobupload ./internal/job ./internal/logprocessor
```

PostgreSQL과 LocalStack 테스트 환경변수 설정 후 전체 업로드 통합 흐름을 실행합니다.

```powershell
go test -tags=integration -v ./cmd/worker -run TestUploadedJobIntegration
```

업로드 → 입력 파일·Job 저장 → SQS 수신 → Worker 분석 → 결과 JSON 생성·성공 메시지 삭제와
잘못된 로그의 FAILED 전이, 발행 실패 시 입력 파일 보존을 검증합니다. 아래의 통합 테스트
절을 참고해 전용 테스트 DB 및 LocalStack 환경변수를 설정합니다.

### 단위 테스트

Docker와 PostgreSQL 없이 실행합니다.

```powershell
go test ./...
```

### PostgreSQL 통합 테스트

`integration` 빌드 태그로 별도 실행합니다. 테스트 전용 PostgreSQL DB의
접속 URL을 `TEST_DATABASE_URL`에 설정해야 하며, 누락 시 테스트는 실패합니다.
각 실행은 고유한 스키마를 만들고 기존 up Migration을 적용한 뒤 정리합니다.
테스트 DB 계정에는 스키마 생성 권한이 필요합니다. 개발 DB URL은 사용하지 마세요.

아래 컨테이너는 기존 Compose 컨테이너 및 `postgres_data` 볼륨과 독립적입니다.
비밀번호는 테스트 실행 시 입력하며 파일에 저장하거나 커밋하지 않습니다.

```powershell
$testCredential = Get-Credential -UserName cloudqueue_test -Message '테스트 DB용 임시 비밀번호'
$env:POSTGRES_PASSWORD = $testCredential.GetNetworkCredential().Password
docker run --detach --rm --name cloudqueue-job-test -p 127.0.0.1:55433:5432 -e POSTGRES_USER=cloudqueue_test -e POSTGRES_DB=cloudqueue_test -e POSTGRES_PASSWORD postgres:17-alpine
docker exec cloudqueue-job-test pg_isready -U cloudqueue_test -d cloudqueue_test
```

`pg_isready`가 연결 가능 상태를 반환한 후 실행합니다.

```powershell
$testPassword = [uri]::EscapeDataString($env:POSTGRES_PASSWORD)
$env:TEST_DATABASE_URL = "postgres://cloudqueue_test:${testPassword}@localhost:55433/cloudqueue_test?sslmode=disable"
go test -tags=integration -v ./internal/job
go test -tags=integration -v ./cmd/worker -run TestLocalWorkerIntegration
docker stop cloudqueue-job-test
Remove-Item Env:TEST_DATABASE_URL, Env:POSTGRES_PASSWORD
```

`-race` 검사는 CGO와 C 컴파일러가 있는 환경에서 실행할 수 있습니다.

### LocalStack SQS 통합 테스트

LocalStack을 실행한 뒤 테스트 endpoint를 지정합니다. 통합 테스트는 실행마다 고유한
Queue를 생성하고 자신이 만든 Queue만 정리합니다.

```powershell
$env:TEST_SQS_ENDPOINT_URL = "http://localhost:4566"
$env:TEST_AWS_REGION = "ap-northeast-2"
go test -tags=integration -v ./internal/jobqueue/...
Remove-Item Env:TEST_SQS_ENDPOINT_URL, Env:TEST_AWS_REGION
```

테스트는 메시지 발행, 수신, 삭제, 잘못된 body 거부 및 long polling Context 취소를
검증합니다.

### Job 생성 API·큐 Worker·PostgreSQL·SQS·S3 통합 테스트

위의 테스트용 PostgreSQL과 LocalStack을 함께 실행한 상태에서 두 테스트 환경변수를
모두 설정합니다. 테스트는 HTTP 요청으로 생성된 Job이 PostgreSQL에 `PENDING`으로
저장되고 동일한 Job ID가 LocalStack SQS에서 수신되는지 검증합니다. 큐 Worker 테스트는
성공한 Job이 `COMPLETED`로 바뀌고 메시지가 삭제되는지, 실패한 Job이 `FAILED`로 바뀌고
메시지는 재전달을 위해 남는지도 확인합니다. 또한 발행 실패 시 API가 내부 상세를 숨긴
`500`을 반환하고 저장된 `PENDING` Job이 남는 현재 동작을 확인합니다.

```powershell
$env:TEST_DATABASE_URL = "postgres://cloudqueue_test:${testPassword}@localhost:55433/cloudqueue_test?sslmode=disable"
$env:TEST_SQS_ENDPOINT_URL = "http://localhost:4566"
$env:TEST_AWS_REGION = "ap-northeast-2"
$env:TEST_S3_ENDPOINT_URL = "http://localhost:4566"
go test -tags=integration -v ./internal/jobsubmission ./cmd/worker
Remove-Item Env:TEST_DATABASE_URL, Env:TEST_SQS_ENDPOINT_URL, Env:TEST_S3_ENDPOINT_URL, Env:TEST_AWS_REGION
```

## 종료

컨테이너를 종료하되 PostgreSQL 데이터를 유지합니다.

```cmd
docker compose down
```

컨테이너와 PostgreSQL 데이터를 모두 삭제합니다.

```cmd
docker compose down -v
```

`down -v`는 로컬 PostgreSQL 데이터를 삭제하므로 개발 데이터가 필요하지 않을
때만 사용하세요.
