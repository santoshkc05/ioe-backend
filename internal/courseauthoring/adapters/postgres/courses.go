package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type courses struct{ q *sqlcgen.Queries }

func (r courses) FindByID(ctx context.Context, courseID id.ID) (domain.Course, error) {
	cs, err := r.load(ctx, []int64{int64(courseID)})
	if err != nil {
		return domain.Course{}, err
	}
	if len(cs) == 0 {
		return domain.Course{}, app.ErrNotFound
	}
	return cs[0], nil
}

func (r courses) ListByOwner(ctx context.Context, ownerID id.ID) ([]domain.Course, error) {
	ids, err := r.q.ListCourseIDsByOwner(ctx, int64(ownerID))
	if err != nil {
		return nil, err
	}
	return r.loadOrdered(ctx, ids)
}

// loadOrdered is load that returns the courses in the order of ids.
func (r courses) loadOrdered(ctx context.Context, ids []int64) ([]domain.Course, error) {
	cs, err := r.load(ctx, ids)
	if err != nil {
		return nil, err
	}
	// load returns database order; restore the requested order.
	byID := make(map[int64]domain.Course, len(cs))
	for _, c := range cs {
		byID[int64(c.ID)] = c
	}
	out := make([]domain.Course, 0, len(ids))
	for _, cid := range ids {
		out = append(out, byID[cid])
	}
	return out, nil
}

// load hydrates courses with their sections and lectures in three queries.
func (r courses) load(ctx context.Context, courseIDs []int64) ([]domain.Course, error) {
	if len(courseIDs) == 0 {
		return nil, nil
	}
	rows, err := r.q.ListCoursesByIDs(ctx, courseIDs)
	if err != nil {
		return nil, err
	}
	sections, err := r.q.ListSectionsByCourseIDs(ctx, courseIDs)
	if err != nil {
		return nil, err
	}
	lectures, err := r.q.ListLecturesByCourseIDs(ctx, courseIDs)
	if err != nil {
		return nil, err
	}
	cats, err := r.q.ListCategoriesByCourseIDs(ctx, courseIDs)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Course, 0, len(rows))
	index := make(map[int64]int, len(rows))
	for _, row := range rows {
		ttl, err := contentblocks.NewTitle(row.Title)
		if err != nil {
			return nil, fmt.Errorf("course %d title: %w", row.ID, err)
		}
		index[row.ID] = len(out)
		out = append(out, domain.Course{
			ID: id.ID(row.ID), OwnerID: id.ID(row.OwnerID), Title: ttl, Description: row.Description,
			Level: row.Level, ThumbnailURL: row.ThumbnailUrl, Status: domain.Status(row.Status),
			Price:       domain.Price{AmountMinor: row.PriceAmountMinor, Currency: row.PriceCurrency},
			Tags:        row.Tags,
			SubmittedAt: timeOrZero(row.SubmittedAt), ReviewedAt: timeOrZero(row.ReviewedAt), ReviewNote: row.ReviewNote,
			LastVersion: int(row.LastVersion), Live: liveVersion(row.LiveVersion, row.LivePublishedAt),
			Version: row.Version, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
	}
	for _, s := range sections {
		ttl, err := contentblocks.NewTitle(s.Title)
		if err != nil {
			return nil, fmt.Errorf("section %d title: %w", s.ID, err)
		}
		c := &out[index[s.CourseID]]
		c.Sections = append(c.Sections, domain.Section{ID: id.ID(s.ID), Title: ttl, Order: int(s.SortOrder)})
	}
	for _, l := range lectures {
		ttl, err := contentblocks.NewTitle(l.Title)
		if err != nil {
			return nil, fmt.Errorf("lecture %d title: %w", l.ID, err)
		}
		var sectionID id.ID
		if l.SectionID != nil {
			sectionID = id.ID(*l.SectionID)
		}
		c := &out[index[l.CourseID]]
		c.Lectures = append(c.Lectures, domain.Lecture{ID: id.ID(l.ID), SectionID: sectionID, Title: ttl,
			FreePreview: l.FreePreview, Order: int(l.SortOrder), HasText: l.HasText, HasVideo: l.HasVideo})
	}
	for _, k := range cats {
		c := &out[index[k.CourseID]]
		c.Categories = append(c.Categories, categoryRef(k.ID, k.Name, k.Slug))
	}
	return out, nil
}

func (r courses) ListPublished(ctx context.Context, q app.CatalogQuery) ([]app.CourseSummary, error) {
	rows, err := r.q.ListPublishedCourses(ctx, sqlcgen.ListPublishedCoursesParams{
		After: int64(q.After), Level: q.Level, Price: string(q.Price),
		RowLimit: int32(q.Limit), //nolint:gosec // the service bounds Limit to MaxCatalogLimit+1
	})
	if err != nil {
		return nil, err
	}
	out := make([]app.CourseSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, app.CourseSummary{
			ID: id.ID(row.ID), OwnerID: id.ID(row.OwnerID), Title: row.Title, Description: row.Description,
			Level: row.Level, ThumbnailURL: row.ThumbnailUrl,
			Price:        domain.Price{AmountMinor: row.PriceAmountMinor, Currency: row.PriceCurrency},
			LectureCount: int(row.LectureCount), SectionCount: int(row.SectionCount),
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
	}
	return out, nil
}

func (r courses) ListInReview(ctx context.Context) ([]domain.Course, error) {
	ids, err := r.q.ListInReviewCourseIDs(ctx)
	if err != nil {
		return nil, err
	}
	return r.loadOrdered(ctx, ids)
}

func (r courses) LockForUpdate(ctx context.Context, courseID id.ID) error {
	_, err := r.q.LockCourseForUpdate(ctx, int64(courseID))
	return notFound(err)
}

func (r courses) InsertVersion(ctx context.Context, c *domain.Course, publishedBy id.ID) error {
	cid, number := int64(c.ID), int32(c.Live.Number) //nolint:gosec // one per publish, far below int32
	if err := r.q.InsertCourseVersion(ctx, sqlcgen.InsertCourseVersionParams{
		CourseID: cid, Number: number, Title: c.Title.String(), Description: c.Description,
		Level: c.Level, ThumbnailUrl: c.ThumbnailURL,
		PriceAmountMinor: c.Price.AmountMinor, PriceCurrency: c.Price.Currency,
		PublishedBy: int64(publishedBy), PublishedAt: c.Live.PublishedAt,
		Tags: nonNilTags(c.Tags),
	}); err != nil {
		return err
	}
	if err := r.q.InsertVersionCategories(ctx, sqlcgen.InsertVersionCategoriesParams{
		CourseID: cid, Number: number, CategoryIds: int64s(c.CategoryIDs())}); err != nil {
		return unknownCategory(err)
	}
	if err := r.q.CopyVersionSections(ctx, sqlcgen.CopyVersionSectionsParams{CourseID: cid, Number: number}); err != nil {
		return err
	}
	if err := r.q.CopyVersionLectures(ctx, sqlcgen.CopyVersionLecturesParams{CourseID: cid, Number: number}); err != nil {
		return err
	}
	return r.q.CopyVersionBlocks(ctx, sqlcgen.CopyVersionBlocksParams{CourseID: cid, Number: number})
}

func (r courses) ListVersions(ctx context.Context, courseID id.ID) ([]app.VersionSummary, error) {
	rows, err := r.q.ListCourseVersions(ctx, int64(courseID))
	if err != nil {
		return nil, err
	}
	out := make([]app.VersionSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, app.VersionSummary{Number: int(row.Number), PublishedBy: id.ID(row.PublishedBy), PublishedAt: row.PublishedAt.UTC()})
	}
	return out, nil
}

func (r courses) FindVersion(ctx context.Context, courseID id.ID, versionNumber int) (domain.Course, error) {
	c, err := r.FindByID(ctx, courseID)
	if err != nil {
		return domain.Course{}, err
	}
	if versionNumber < 1 || versionNumber > c.LastVersion {
		return domain.Course{}, app.ErrNotFound
	}
	cid, number := int64(courseID), int32(versionNumber) //nolint:gosec // bounded by LastVersion
	v, err := r.q.GetCourseVersion(ctx, sqlcgen.GetCourseVersionParams{CourseID: cid, Number: number})
	if err != nil {
		return domain.Course{}, notFound(err)
	}
	sections, err := r.q.ListVersionSections(ctx, sqlcgen.ListVersionSectionsParams{CourseID: cid, Number: number})
	if err != nil {
		return domain.Course{}, err
	}
	lectures, err := r.q.ListVersionLectures(ctx, sqlcgen.ListVersionLecturesParams{CourseID: cid, Number: number})
	if err != nil {
		return domain.Course{}, err
	}
	cats, err := r.q.ListVersionCategories(ctx, sqlcgen.ListVersionCategoriesParams{CourseID: cid, Number: number})
	if err != nil {
		return domain.Course{}, err
	}
	ttl, err := contentblocks.NewTitle(v.Title)
	if err != nil {
		return domain.Course{}, fmt.Errorf("course %d version %d title: %w", courseID, v.Number, err)
	}
	live := domain.Course{
		ID: c.ID, OwnerID: c.OwnerID, Title: ttl, Description: v.Description, Level: v.Level,
		ThumbnailURL: v.ThumbnailUrl, Price: domain.Price{AmountMinor: v.PriceAmountMinor, Currency: v.PriceCurrency},
		Tags:   v.Tags,
		Status: domain.StatusPublished, LastVersion: c.LastVersion, Live: c.Live, Version: c.Version,
		CreatedAt: c.CreatedAt, UpdatedAt: v.PublishedAt.UTC(),
		Sections: make([]domain.Section, 0, len(sections)), Lectures: make([]domain.Lecture, 0, len(lectures)),
	}
	for _, s := range sections {
		st, err := contentblocks.NewTitle(s.Title)
		if err != nil {
			return domain.Course{}, fmt.Errorf("section %d title: %w", s.ID, err)
		}
		live.Sections = append(live.Sections, domain.Section{ID: id.ID(s.ID), Title: st, Order: int(s.SortOrder)})
	}
	for _, l := range lectures {
		lt, err := contentblocks.NewTitle(l.Title)
		if err != nil {
			return domain.Course{}, fmt.Errorf("lecture %d title: %w", l.ID, err)
		}
		var sectionID id.ID
		if l.SectionID != nil {
			sectionID = id.ID(*l.SectionID)
		}
		live.Lectures = append(live.Lectures, domain.Lecture{ID: id.ID(l.ID), SectionID: sectionID, Title: lt,
			FreePreview: l.FreePreview, Order: int(l.SortOrder), HasText: l.HasText, HasVideo: l.HasVideo})
	}
	for _, k := range cats {
		live.Categories = append(live.Categories, categoryRef(k.ID, k.Name, k.Slug))
	}
	return live, nil
}

func (r courses) InsertReview(ctx context.Context, rev domain.Review) error {
	return r.q.InsertCourseReview(ctx, sqlcgen.InsertCourseReviewParams{
		ID: int64(rev.ID), CourseID: int64(rev.CourseID), ActorID: int64(rev.ActorID),
		Decision: string(rev.Decision), Note: rev.Note, CreatedAt: rev.CreatedAt,
	})
}

func (r courses) ListReviews(ctx context.Context, courseID id.ID) ([]domain.Review, error) {
	rows, err := r.q.ListCourseReviews(ctx, int64(courseID))
	if err != nil {
		return nil, err
	}
	out := make([]domain.Review, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.Review{ID: id.ID(row.ID), CourseID: id.ID(row.CourseID), ActorID: id.ID(row.ActorID),
			Decision: domain.ReviewDecision(row.Decision), Note: row.Note, CreatedAt: row.CreatedAt.UTC()})
	}
	return out, nil
}

func (r courses) Insert(ctx context.Context, c *domain.Course) error {
	if err := r.q.InsertCourse(ctx, sqlcgen.InsertCourseParams{
		ID: int64(c.ID), OwnerID: int64(c.OwnerID), Title: c.Title.String(), Description: c.Description,
		Level: c.Level, ThumbnailUrl: c.ThumbnailURL, Status: string(c.Status),
		PriceAmountMinor: c.Price.AmountMinor, PriceCurrency: c.Price.Currency,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, Tags: nonNilTags(c.Tags),
	}); err != nil {
		return err
	}
	c.Version = 1
	return r.saveChildren(ctx, c)
}

func (r courses) Update(ctx context.Context, c *domain.Course) error {
	n, err := r.q.UpdateCourse(ctx, sqlcgen.UpdateCourseParams{
		ID: int64(c.ID), Version: c.Version, Title: c.Title.String(), Description: c.Description,
		Level: c.Level, ThumbnailUrl: c.ThumbnailURL, Status: string(c.Status),
		PriceAmountMinor: c.Price.AmountMinor, PriceCurrency: c.Price.Currency, UpdatedAt: c.UpdatedAt,
		SubmittedAt: optionalTime(c.SubmittedAt), ReviewedAt: optionalTime(c.ReviewedAt), ReviewNote: c.ReviewNote,
		LastVersion: int32(c.LastVersion), //nolint:gosec // one per publish, far below int32
		LiveVersion: optionalVersion(c.Live.Number),
		Tags:        nonNilTags(c.Tags),
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return app.ErrConcurrentModification
	}
	c.Version++
	return r.saveChildren(ctx, c)
}

// saveChildren deletes rows no longer in the aggregate, then upserts sections and
// lectures. Deleting first lets a restored section reuse a title a removed one held. It
// never touches lecture content or content_revision.
func (r courses) saveChildren(ctx context.Context, c *domain.Course) error {
	sectionIDs := make([]int64, 0, len(c.Sections))
	for _, s := range c.Sections {
		sectionIDs = append(sectionIDs, int64(s.ID))
	}
	lectureIDs := make([]int64, 0, len(c.Lectures))
	for _, l := range c.Lectures {
		lectureIDs = append(lectureIDs, int64(l.ID))
	}
	if err := r.q.DeleteLecturesNotIn(ctx, sqlcgen.DeleteLecturesNotInParams{CourseID: int64(c.ID), Keep: lectureIDs}); err != nil {
		return err
	}
	if err := r.q.DeleteSectionsNotIn(ctx, sqlcgen.DeleteSectionsNotInParams{CourseID: int64(c.ID), Keep: sectionIDs}); err != nil {
		return err
	}
	for _, s := range c.Sections {
		if err := r.q.UpsertSection(ctx, sqlcgen.UpsertSectionParams{
			ID: int64(s.ID), CourseID: int64(c.ID), Title: s.Title.String(),
			SortOrder: int32(s.Order), //nolint:gosec // Order is a slice index, bounded by aggregate size
		}); err != nil {
			return err
		}
	}
	for _, l := range c.Lectures {
		var sectionID *int64
		if !l.SectionID.IsZero() {
			v := int64(l.SectionID)
			sectionID = &v
		}
		if err := r.q.UpsertLecture(ctx, sqlcgen.UpsertLectureParams{
			ID: int64(l.ID), CourseID: int64(c.ID), SectionID: sectionID, Title: l.Title.String(),
			FreePreview: l.FreePreview, CreatedAt: c.UpdatedAt,
			SortOrder: int32(l.Order), //nolint:gosec // Order is a slice index, bounded by aggregate size
		}); err != nil {
			return err
		}
	}
	if err := r.q.DeleteCourseCategories(ctx, int64(c.ID)); err != nil {
		return err
	}
	if err := r.q.InsertCourseCategories(ctx, sqlcgen.InsertCourseCategoriesParams{
		CourseID: int64(c.ID), CategoryIds: int64s(c.CategoryIDs())}); err != nil {
		return unknownCategory(err)
	}
	return nil
}

func liveVersion(number *int32, publishedAt *time.Time) domain.LiveVersion {
	if number == nil {
		return domain.LiveVersion{}
	}
	return domain.LiveVersion{Number: int(*number), PublishedAt: timeOrZero(publishedAt)}
}

func optionalVersion(number int) *int32 {
	if number == 0 {
		return nil
	}
	v := int32(number) //nolint:gosec // one per publish, far below int32
	return &v
}

func timeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrNotFound
	}
	return err
}

func pinArgs(pins []domain.AssessmentPin) ([]string, []int64, []int32) {
	kinds, ids, revs := make([]string, len(pins)), make([]int64, len(pins)), make([]int32, len(pins))
	for i, p := range pins {
		kinds[i], ids[i], revs[i] = string(p.Kind), int64(p.ID), int32(p.Revision) //nolint:gosec // revisions grow one per edit
	}
	return kinds, ids, revs
}

func (r courses) ReplaceSubmittedPins(ctx context.Context, courseID id.ID, pins []domain.AssessmentPin) error {
	if err := r.q.DeleteSubmittedAssessments(ctx, int64(courseID)); err != nil {
		return err
	}
	kinds, ids, revs := pinArgs(pins)
	return r.q.InsertSubmittedAssessments(ctx, sqlcgen.InsertSubmittedAssessmentsParams{
		CourseID: int64(courseID), Kinds: kinds, AssessmentIds: ids, Revisions: revs})
}

func (r courses) ListSubmittedPins(ctx context.Context, courseID id.ID) ([]domain.AssessmentPin, error) {
	rows, err := r.q.ListSubmittedAssessments(ctx, int64(courseID))
	if err != nil {
		return nil, err
	}
	out := make([]domain.AssessmentPin, len(rows))
	for i, row := range rows {
		out[i] = domain.AssessmentPin{Kind: domain.AssessmentKind(row.Kind), ID: id.ID(row.AssessmentID), Revision: int(row.Revision)}
	}
	return out, nil
}

func (r courses) InsertVersionPins(ctx context.Context, courseID id.ID, number int, pins []domain.AssessmentPin) error {
	kinds, ids, revs := pinArgs(pins)
	return r.q.InsertVersionAssessments(ctx, sqlcgen.InsertVersionAssessmentsParams{
		CourseID: int64(courseID), Number: int32(number), Kinds: kinds, AssessmentIds: ids, Revisions: revs}) //nolint:gosec // bounded by LastVersion
}

func (r courses) ListVersionPins(ctx context.Context, courseID id.ID, number int) ([]domain.AssessmentPin, error) {
	rows, err := r.q.ListVersionAssessments(ctx, sqlcgen.ListVersionAssessmentsParams{CourseID: int64(courseID), Number: int32(number)}) //nolint:gosec // bounded by LastVersion
	if err != nil {
		return nil, err
	}
	out := make([]domain.AssessmentPin, len(rows))
	for i, row := range rows {
		out[i] = domain.AssessmentPin{Kind: domain.AssessmentKind(row.Kind), ID: id.ID(row.AssessmentID), Revision: int(row.Revision)}
	}
	return out, nil
}

// nonNilTags keeps a nil slice from being written as NULL into a NOT NULL text[] column.
func nonNilTags(tags []string) []string {
	if tags == nil {
		return []string{}
	}
	return tags
}

// unknownCategory maps a foreign-key failure on a category link, a category deleted after the
// caller checked it, to app.ErrUnknownCategory.
func unknownCategory(err error) error {
	if pgCode(err) == foreignKeyViolation {
		return app.ErrUnknownCategory
	}
	return err
}
