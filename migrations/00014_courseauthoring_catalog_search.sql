-- +goose Up
CREATE TABLE courseauthoring.categories (
  id         bigint PRIMARY KEY,
  name       text NOT NULL,
  slug       text NOT NULL UNIQUE,
  created_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX categories_name_key ON courseauthoring.categories (lower(name));

ALTER TABLE courseauthoring.courses ADD COLUMN tags text[] NOT NULL DEFAULT '{}';

CREATE TABLE courseauthoring.course_categories (
  course_id   bigint NOT NULL REFERENCES courseauthoring.courses (id) ON DELETE CASCADE,
  category_id bigint NOT NULL REFERENCES courseauthoring.categories (id) ON DELETE CASCADE,
  position    integer NOT NULL,
  PRIMARY KEY (course_id, category_id)
);
CREATE INDEX course_categories_category_idx ON courseauthoring.course_categories (category_id);

-- The searchable document of a published version. The text search configuration is chosen
-- here only; supporting another language means replacing this function and re-adding the
-- search column. array_to_string is STABLE in general but immutable for text[], so the
-- function may be IMMUTABLE and back a generated column.
-- +goose StatementBegin
CREATE FUNCTION courseauthoring.course_search_document(title text, tags text[], description text)
RETURNS tsvector LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
  SELECT setweight(to_tsvector('simple', title), 'A') ||
         setweight(to_tsvector('simple', array_to_string(tags, ' ')), 'B') ||
         setweight(to_tsvector('simple', description), 'C')
$$;
-- +goose StatementEnd

ALTER TABLE courseauthoring.course_versions
  ADD COLUMN tags text[] NOT NULL DEFAULT '{}',
  ADD COLUMN search tsvector GENERATED ALWAYS AS
    (courseauthoring.course_search_document(title, tags, description)) STORED;

CREATE TABLE courseauthoring.course_version_categories (
  course_id   bigint NOT NULL,
  number      integer NOT NULL,
  category_id bigint NOT NULL REFERENCES courseauthoring.categories (id) ON DELETE CASCADE,
  position    integer NOT NULL,
  PRIMARY KEY (course_id, number, category_id),
  FOREIGN KEY (course_id, number)
    REFERENCES courseauthoring.course_versions (course_id, number) ON DELETE CASCADE
);
CREATE INDEX course_version_categories_category_idx
  ON courseauthoring.course_version_categories (category_id);

CREATE INDEX course_versions_search_idx ON courseauthoring.course_versions USING gin (search);
CREATE INDEX course_versions_tags_idx ON courseauthoring.course_versions USING gin (tags);

-- +goose Down
DROP TABLE courseauthoring.course_version_categories;
ALTER TABLE courseauthoring.course_versions DROP COLUMN search, DROP COLUMN tags;
DROP FUNCTION courseauthoring.course_search_document(text, text[], text);
DROP TABLE courseauthoring.course_categories;
ALTER TABLE courseauthoring.courses DROP COLUMN tags;
DROP TABLE courseauthoring.categories;
