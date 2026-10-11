-- Mirrors the Cloud SQL users created by Terraform (local passwords only).
CREATE ROLE ticketing_app LOGIN PASSWORD 'local-app-password';
CREATE ROLE lab_observer LOGIN PASSWORD 'local-observer-password';
CREATE DATABASE ticketing OWNER ticketing_app;
