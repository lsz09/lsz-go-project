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
- [ ] 비동기 Worker
- [ ] 메시지 큐
- [ ] 파일 업로드
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

성공 시 `201 Created`와 생성된 `PENDING` 작업을 반환합니다.

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
- 현재 구현 범위는 Worker 코어이며 실행 프로세스, SQS 연동, 파일 입출력과 Processor
  연결 및 재시도는 이후 작업에서 추가합니다.

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
시작하지 않습니다. 현재 구현은 분석 코어만 포함하며 로컬 파일 입출력, Worker 연결 및
S3 저장은 후속 작업에서 추가합니다.

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
docker stop cloudqueue-job-test
Remove-Item Env:TEST_DATABASE_URL, Env:POSTGRES_PASSWORD
```

`-race` 검사는 CGO와 C 컴파일러가 있는 환경에서 실행할 수 있습니다.

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
