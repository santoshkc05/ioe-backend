package main

import (
	"context"
	"errors"
	"log/slog"

	courseauthoringapp "github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	courseauthoringdomain "github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	mediahttp "github.com/santoshkc2200/ioe-backend/internal/media/adapters/httpapi"
	"github.com/santoshkc2200/ioe-backend/internal/media/adapters/mediasvc"
	mediapg "github.com/santoshkc2200/ioe-backend/internal/media/adapters/postgres"
	mediaapp "github.com/santoshkc2200/ioe-backend/internal/media/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/config"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// mediaAssetCatalog lets course authoring validate asset references against media.
type mediaAssetCatalog struct{ query *mediaapp.AssetQuery }

func (c mediaAssetCatalog) Kinds(ctx context.Context, courseID id.ID, ids []id.ID) (map[id.ID]courseauthoringapp.AssetKind, error) {
	kinds, err := c.query.Kinds(ctx, courseID, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[id.ID]courseauthoringapp.AssetKind, len(kinds))
	for k, v := range kinds {
		out[k] = courseauthoringapp.AssetKind(v)
	}
	return out, nil
}

// mediaCourseAccess lets media ask course authoring who may manage a course or read a lecture.
type mediaCourseAccess struct {
	courses  *courseauthoringapp.CourseService
	contents *courseauthoringapp.ContentService
}

func (a mediaCourseAccess) CanManage(ctx context.Context, p auth.Principal, courseID id.ID) error {
	return toMediaError(a.courses.CheckManage(ctx, p, courseID))
}

func (a mediaCourseAccess) CanReadLectureAsset(ctx context.Context, p auth.Principal, courseID, lectureID, assetID id.ID) error {
	return toMediaError(a.contents.CheckAssetRead(ctx, p, courseID, lectureID, assetID))
}

func toMediaError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, courseauthoringapp.ErrNotFound):
		return mediaapp.ErrNotFound
	case errors.Is(err, courseauthoringapp.ErrForbidden):
		return mediaapp.ErrForbidden
	case errors.Is(err, courseauthoringapp.ErrEnrollmentRequired):
		return mediaapp.ErrEnrollmentRequired
	case errors.Is(err, courseauthoringdomain.ErrCourseNotEditable):
		return mediaapp.ErrCourseNotEditable
	}
	return err
}

// registerMedia mounts media routes when the media service is configured.
func registerMedia(r *httpserver.Router, assets *mediapg.Assets, courses *courseauthoringapp.CourseService, contents *courseauthoringapp.ContentService, ids *id.Generator, clk clock.Clock, cfg config.Config, requireAuth httpserver.Middleware, logger *slog.Logger) {
	if !cfg.MediaEnabled() {
		logger.Warn("media uploads disabled: MEDIA_SERVICE_BASE_URL, MEDIA_SERVICE_PUBLIC_URL and MEDIA_SERVICE_API_KEY are not set")
		return
	}
	remote := mediasvc.New(cfg.MediaServiceBaseURL, cfg.MediaServicePublicURL, cfg.MediaServiceAPIKey)
	svc := mediaapp.NewAssetService(assets, remote, mediaCourseAccess{courses: courses, contents: contents}, ids, clk)
	mediahttp.New(svc, mediahttp.Config{
		RequireAuth: requireAuth, CreateLimiter: httpserver.NewRateLimiter(30), Logger: logger,
	}).Register(r)
}
