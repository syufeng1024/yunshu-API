-- Local development schemas for MySQL 8.0.
-- The collation is case-sensitive on purpose: model names, aliases, and key
-- names distinguish qwen-Max from qwen-max.
--
-- This file runs only when the mysql_data volume is empty. No application
-- password is stored here; local processes use the compose root credential
-- from MYSQL_ROOT_PASSWORD (.env.example documents the dev-only default).

CREATE DATABASE IF NOT EXISTS `yunshu`
  DEFAULT CHARACTER SET utf8mb4
  DEFAULT COLLATE utf8mb4_0900_as_cs;

CREATE DATABASE IF NOT EXISTS `yunshu_log`
  DEFAULT CHARACTER SET utf8mb4
  DEFAULT COLLATE utf8mb4_0900_as_cs;
