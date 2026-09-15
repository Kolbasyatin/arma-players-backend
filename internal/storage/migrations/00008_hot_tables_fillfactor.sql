-- +goose Up
-- Горячие таблицы: их строки обновляются постоянно, а Postgres при обновлении пишет НОВУЮ версию
-- строки и помечает старую мёртвой. Если новая версия не помещается на ту же страницу, приходится
-- писать её на другую и обновлять все индексы — это и есть распухание. На проде 150 тысяч строк
-- player_alias занимали 586 МБ вместо ~20 МБ: 4 килобайта на строку в сто байт.
--
-- fillfactor 85 оставляет на странице свободное место, чтобы новая версия легла рядом со старой
-- (HOT-обновление): индексы при этом не трогаются, а место переиспользуется сразу.
-- 85, а не меньше: таблицы небольшие, и лишние 15% места дешевле, чем обновление индексов.
--
-- Автовакуум для них настроен агрессивнее умолчания (20% таблицы): при миллионах обновлений
-- в сутки ждать, пока накопится пятая часть таблицы, поздно.
ALTER TABLE player_identity          SET (fillfactor = 85, autovacuum_vacuum_scale_factor = 0.02, autovacuum_vacuum_cost_limit = 1000);
ALTER TABLE player_alias             SET (fillfactor = 85, autovacuum_vacuum_scale_factor = 0.02, autovacuum_vacuum_cost_limit = 1000);
ALTER TABLE player_platform_identity SET (fillfactor = 85, autovacuum_vacuum_scale_factor = 0.02, autovacuum_vacuum_cost_limit = 1000);
-- Сессии обновляются КАЖДЫМ опросом и по-другому не могут: на их last_seen_at держится точность
-- присутствия. Поэтому для них fillfactor важнее всего.
ALTER TABLE player_server_session    SET (fillfactor = 85, autovacuum_vacuum_scale_factor = 0.02, autovacuum_vacuum_cost_limit = 1000);

-- +goose Down
ALTER TABLE player_identity          RESET (fillfactor, autovacuum_vacuum_scale_factor, autovacuum_vacuum_cost_limit);
ALTER TABLE player_alias             RESET (fillfactor, autovacuum_vacuum_scale_factor, autovacuum_vacuum_cost_limit);
ALTER TABLE player_platform_identity RESET (fillfactor, autovacuum_vacuum_scale_factor, autovacuum_vacuum_cost_limit);
ALTER TABLE player_server_session    RESET (fillfactor, autovacuum_vacuum_scale_factor, autovacuum_vacuum_cost_limit);
