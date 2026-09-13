package job

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// CreateRepository는 작업 생성 Service가 사용하는 최소 Repository 기능입니다.
type CreateRepository interface {
	Create(ctx context.Context, params CreateParams) (Job, error)
}

// Service는 Job 비즈니스 규칙을 Repository와 HTTP 계층 사이에서 처리합니다.
type Service struct {
	repository CreateRepository
}

// NewService는 주입받은 Repository로 Job Service를 생성합니다.
func NewService(repository CreateRepository) *Service {
	return &Service{repository: repository}
}

// Create는 입력을 검증한 뒤 Repository에 PENDING 작업 생성을 요청합니다.
func (s *Service) Create(ctx context.Context, params CreateParams) (Job, error) {
	// 빈 문자열과 공백만 있는 파일 이름을 비즈니스 입력 오류로 처리합니다.
	if strings.TrimSpace(params.FileName) == "" {
		return Job{}, fmt.Errorf("create job service: %w: file name is required", ErrInvalidInput)
	}

	// 잘못 조립된 애플리케이션이 nil Repository를 호출해 panic을 내지 않도록 방어합니다.
	if s == nil || s.repository == nil {
		return Job{}, errors.New("create job service: repository is required")
	}

	// 호출자의 Context와 원본 입력을 변경하지 않고 Repository에 전달합니다.
	created, err := s.repository.Create(ctx, params)
	if err != nil {
		return Job{}, fmt.Errorf("create job service: %w", err)
	}

	return created, nil
}
