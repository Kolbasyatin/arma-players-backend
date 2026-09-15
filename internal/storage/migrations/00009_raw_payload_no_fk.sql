-- +goose Up
-- Снимаем внешний ключ server_observation.raw_payload_id → raw_payload.id.
--
-- Он приносил два вреда и ни одной пользы:
--
-- 1. Каждое удаление просроченного сырья тянуло за собой UPDATE снимков (ON DELETE SET NULL).
--    Чистка 468 тысяч строк означала сотни тысяч обновлений server_observation — то есть новую
--    порцию распухания там, где мы от него и лечимся.
-- 2. Ссылка делала невозможной штатную очистку: TRUNCATE отказывается работать с таблицей,
--    на которую ссылаются, а CASCADE снёс бы и сами снимки.
--
-- Целостность от этого не страдает: raw_payload — отладочный буфер с ограниченным сроком жизни,
-- и «ссылка в никуда» здесь нормальное состояние, а не ошибка. Код и так читает сырьё только
-- если строка нашлась, а при RAW_STORE=off колонка почти всегда NULL.
ALTER TABLE server_observation DROP CONSTRAINT IF EXISTS server_observation_raw_payload_id_fkey;
COMMENT ON COLUMN server_observation.raw_payload_id IS 'Сырой ответ, из которого получен снимок. Не внешний ключ: сырьё живёт ограниченное время и удаляется независимо, поэтому ссылка может указывать в никуда. NULL, если сырьё не сохранялось (RAW_STORE=off).';

-- +goose Down
-- Восстановить ключ можно только обнулив ссылки на уже удалённое сырьё.
UPDATE server_observation o SET raw_payload_id = NULL
WHERE raw_payload_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM raw_payload r WHERE r.id = o.raw_payload_id);
ALTER TABLE server_observation
    ADD CONSTRAINT server_observation_raw_payload_id_fkey
    FOREIGN KEY (raw_payload_id) REFERENCES raw_payload (id) ON DELETE SET NULL;
