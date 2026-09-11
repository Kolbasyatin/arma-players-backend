-- +goose Up
-- Запрос «когда последний раз успешно опрашивали этот сервер» выполняется на КАЖДЫЙ poll каждого
-- сервера (presence спрашивает его, чтобы отличить разрыв наблюдения от обычного отсутствия игрока).
-- Без этого индекса Postgres берёт все строки poll_run по server_id и сортирует их по finished_at:
-- на 123 тысячах строк это уже 14 мс, а таблица растёт на ~290 тысяч строк в сутки при 100 серверах.
--
-- Частичный индекс, а не полный: условие по типу и статусу зашито в него самого, поэтому он
-- в разы меньше и обслуживает ровно этот вопрос. finished_at DESC даёт ответ первой же строкой,
-- без сортировки.
CREATE INDEX poll_run_last_success_idx ON poll_run (server_id, finished_at DESC)
    WHERE poll_type = 'LIST_PLAYERS' AND status = 'SUCCESS';
COMMENT ON INDEX poll_run_last_success_idx IS 'Последний успешный listPlayers по серверу: спрашивается на каждом poll для определения разрыва наблюдения.';

-- +goose Down
DROP INDEX poll_run_last_success_idx;
