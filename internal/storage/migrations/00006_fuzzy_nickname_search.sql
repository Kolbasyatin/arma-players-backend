-- +goose Up
-- Нечёткий поиск по нику: человек вводит ник по памяти и ошибается в регистре, раскладке или букве.
-- pg_trgm сравнивает строки по триграммам; GIN-индекс делает это быстрым на десятках тысяч алиасов.
-- Требует прав суперпользователя. В нашей раскладке владелец БД им и является (POSTGRES_USER
-- контейнера), проверено на проде. Если когда-нибудь observer пойдёт под ограниченной ролью,
-- расширение нужно поставить заранее руками: иначе миграция не пройдёт и сервис не стартует.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX player_alias_nickname_trgm_idx ON player_alias USING gin (lower(nickname) gin_trgm_ops);
COMMENT ON INDEX player_alias_nickname_trgm_idx IS 'Триграммный индекс для поиска похожих ников (similarity), когда точных совпадений по подстроке нет.';

-- +goose Down
DROP INDEX player_alias_nickname_trgm_idx;
