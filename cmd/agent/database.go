package main

import (
	"errors"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openAgentDatabase(dsn string) (*gorm.DB, error) {
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("invalid AGENT_MYSQL_DSN")
	}
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 3*time.Second, 2*time.Second, 2*time.Second
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, errors.New("cannot connect to Agent database; check AGENT_MYSQL_DSN and MySQL")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(5)
	sqlDB.SetMaxIdleConns(2)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)
	return db, nil
}
