-- CallGo.mn — executed once by the postgres image on an EMPTY data volume.
-- Creates the database used by `make backend-test` (CALLGO_TEST_DATABASE_URL).
-- Тестийн өгөгдлийн сан үүсгэнэ.
CREATE DATABASE callgo_test OWNER callgo;
