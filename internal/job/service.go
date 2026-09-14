package job

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ServiceRepository는 Job Service가 사용하는 Repository 기능입니다.
type ServiceRepository interface {
	Create(ctx context.Context, params CreateParams) (Job, error)
	FindByID(ctx context.Context, id string) (Job, error)
	List(ctx context.Context, options ListOptions) ([]Job, error)
}

// Service는 Job 비즈니스 규칙을 Repository와 HTTP 계층 사이에서 처리합니다.
type Service struct {
	repository ServiceRepository
}

// NewService는 주입받은 Repository로 Job Service를 생성합니다.
func NewService(repository ServiceRepository) *Service {
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

// List는 페이지 옵션을 Repository에 전달하고 조회 결과를 반환합니다.
func (s *Service) List(ctx context.Context, options ListOptions) ([]Job, error) {
	// 잘못 조립된 애플리케이션이 nil Repository를 호출해 panic을 내지 않도록 방어합니다.
	if s == nil || s.repository == nil {
		return nil, errors.New("list jobs service: repository is required")
	}

	// 호출자의 Context와 페이지 옵션을 변경하지 않고 Repository에 전달합니다.
	jobs, err := s.repository.List(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("list jobs service: %w", err)
	}

	return jobs, nil
}

// FindByID는 작업 ID를 Repository에 전달하고 단건 조회 결과를 반환합니다.
func (s *Service) FindByID(ctx context.Context, id string) (Job, error) {
	// 잘못 조립된 애플리케이션이 nil Repository를 호출해 panic을 내지 않도록 방어합니다.
	if s == nil || s.repository == nil {
		return Job{}, errors.New("find job service: repository is required")
	}

	// 호출자의 Context와 작업 ID를 변경하지 않고 Repository에 전달합니다.
	found, err := s.repository.FindByID(ctx, id)
	if err != nil {
		return Job{}, fmt.Errorf("find job service: %w", err)
	}

	return found, nil
}
