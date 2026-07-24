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