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
- [ ] 작업 생성 및 조회 API
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
