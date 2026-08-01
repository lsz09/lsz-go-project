# CloudQueue

Go와 AWS를 활용한 비동기 파일 처리 및 작업 관리 플랫폼입니다.

사용자가 파일 처리 작업을 요청하면 API 서버가 작업을 메시지 큐에 전달하고,
Worker가 비동기로 처리한 후 결과와 작업 상태를 저장합니다.

## 현재 구현 상태

- [x] Go 프로젝트 초기화
- [x] API 서버 실행
- [x] Health Check API
- [ ] PostgreSQL 연결
- [ ] 작업 생성 및 조회 API
- [ ] 비동기 Worker
- [ ] 메시지 큐
- [ ] 파일 업로드
- [ ] AWS 배포
- [ ] 모니터링 및 CI/CD

## 실행 방법

```bash
go run ./cmd/api

## 로컬 실행

### 환경변수 설정

```cmd
copy .env.example .env
```

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

## 상태 확인 API

API 프로세스 상태:

```http
GET /health
```

PostgreSQL을 포함한 요청 처리 준비 상태:

```http
GET /ready
```

PostgreSQL 연결 성공 시 `/ready`는 `200 OK`를 반환합니다.

PostgreSQL 연결 실패 시 `/ready`는 `503 Service Unavailable`을 반환합니다.

## 종료

컨테이너를 종료하되 데이터를 유지합니다.

```cmd
docker compose down
```

컨테이너와 PostgreSQL 데이터를 모두 삭제합니다.

```cmd
docker compose down -v
```