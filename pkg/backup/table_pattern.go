package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/AlexAkulov/clickhouse-backup/pkg/config"
	apexLog "github.com/apex/log"
	"github.com/google/uuid"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/AlexAkulov/clickhouse-backup/pkg/common"
	"github.com/AlexAkulov/clickhouse-backup/pkg/filesystemhelper"

	"github.com/AlexAkulov/clickhouse-backup/pkg/metadata"
)

type ListOfTables []metadata.TableMetadata

// Sort - sorting ListOfTables slice orderly by engine priority
func (lt ListOfTables) Sort(dropTable bool) {
	sort.Slice(lt, func(i, j int) bool {
		return getOrderByEngine(lt[i].Query, dropTable) < getOrderByEngine(lt[j].Query, dropTable)
	})
}

func addTableToListIfNotExistsOrEnrichQueryAndParts(tables ListOfTables, table metadata.TableMetadata) ListOfTables {
	for i, t := range tables {
		if (t.Database == table.Database) && (t.Table == table.Table) {
			if t.Query == "" && table.Query != "" {
				tables[i].Query = table.Query
			}
			if len(t.Parts) == 0 && len(table.Parts) > 0 {
				tables[i].Parts = table.Parts
			}
			return tables
		}
	}
	return append(tables, table)
}

func getTableListByPatternLocal(cfg *config.Config, metadataPath string, tablePattern string, dropTable bool, partitionsFilter common.EmptyMap) (ListOfTables, error) {
	result := ListOfTables{}
	tablePatterns := []string{"*"}
	log := apexLog.WithField("logger", "getTableListByPatternLocal")
	if tablePattern != "" {
		tablePatterns = strings.Split(tablePattern, ",")
	}
	if err := filepath.Walk(metadataPath, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !strings.HasSuffix(filePath, ".sql") &&
			!strings.HasSuffix(filePath, ".json") &&
			!info.Mode().IsRegular() {
			return nil
		}
		p := filepath.ToSlash(filePath)
		isEmbeddedMetadata := false
		if strings.HasSuffix(p, ".sql") {
			isEmbeddedMetadata = true
			p = strings.TrimSuffix(p, ".sql")
		} else {
			p = strings.TrimSuffix(p, ".json")
		}
		p = strings.Trim(strings.TrimPrefix(p, metadataPath), "/")
		names := strings.Split(p, "/")
		if len(names) != 2 {
			return nil
		}
		database, _ := url.PathUnescape(names[0])
		if IsInformationSchema(database) {
			return nil
		}
		table, _ := url.PathUnescape(names[1])
		tableName := fmt.Sprintf("%s.%s", database, table)
		shallSkipped := false
		for _, skipPattern := range cfg.ClickHouse.SkipTables {
			if shallSkipped, _ = filepath.Match(skipPattern, tableName); shallSkipped {
				break
			}
		}
		for _, p := range tablePatterns {
			if matched, _ := filepath.Match(strings.Trim(p, " \t\r\n"), tableName); !matched || shallSkipped {
				continue
			}
			data, err := os.ReadFile(filePath)
			if err != nil {
				return err
			}
			if isEmbeddedMetadata {
				// embedded backup to s3 disk could contain only s3 key names inside .sql file
				query := string(data)
				if strings.HasPrefix(query, "ATTACH") || strings.HasPrefix(query, "CREATE") {
					query = strings.Replace(query, "ATTACH", "CREATE", 1)
				} else {
					query = ""
				}
				dataPartsPath := strings.Replace(metadataPath, "/metadata", "/data", 1)
				dataPartsPath = path.Join(dataPartsPath, path.Join(names...))
				if _, err := os.Stat(dataPartsPath); err != nil && !os.IsNotExist(err) {
					return err
				}
				dataParts, err := os.ReadDir(dataPartsPath)
				if err != nil {
					log.Warn(err.Error())
				}
				parts := map[string][]metadata.Part{
					cfg.ClickHouse.EmbeddedBackupDisk: make([]metadata.Part, len(dataParts)),
				}
				for i := range dataParts {
					parts[cfg.ClickHouse.EmbeddedBackupDisk][i].Name = dataParts[i].Name()
				}
				var t metadata.TableMetadata
				t = metadata.TableMetadata{
					Database: database,
					Table:    table,
					Query:    query,
					Parts:    parts,
				}
				filterPartsByPartitionsFilter(t, partitionsFilter)
				result = addTableToListIfNotExistsOrEnrichQueryAndParts(result, t)

				return nil
			}
			var t metadata.TableMetadata
			if err := json.Unmarshal(data, &t); err != nil {
				return err
			}
			filterPartsByPartitionsFilter(t, partitionsFilter)
			result = addTableToListIfNotExistsOrEnrichQueryAndParts(result, t)
			return nil
		}
		return nil
	}); err != nil {
		return nil, err
	}
	result.Sort(dropTable)
	return result, nil
}

var queryRE = regexp.MustCompile(`(?m)^(CREATE|ATTACH) (TABLE|VIEW|MATERIALIZED VIEW|DICTIONARY|FUNCTION) (\x60?)([^\s\x60.]*)(\x60?)\.([^\s\x60.]*)(?:( TO )(\x60?)([^\s\x60.]*)(\x60?)(\.))?`)
var createRE = regexp.MustCompile(`(?m)^CREATE`)
var attachRE = regexp.MustCompile(`(?m)^ATTACH`)
var uuidRE = regexp.MustCompile(`UUID '[a-f\d\-]+'`)

func changeTableQueryToAdjustDatabaseMapping(originTables *ListOfTables, dbMapRule map[string]string) error {
	for i := 0; i < len(*originTables); i++ {
		originTable := (*originTables)[i]
		if targetDB, isMapped := dbMapRule[originTable.Database]; isMapped {
			// substitute database in the table create query
			var substitution string

			if len(createRE.FindAllString(originTable.Query, -1)) > 0 {
				// matching CREATE... command
				substitution = fmt.Sprintf("${1} ${2} ${3}%v${5}.${6}", targetDB)
			} else if len(attachRE.FindAllString(originTable.Query, -1)) > 0 {
				// matching ATTACH...TO... command
				substitution = fmt.Sprintf("${1} ${2} ${3}%v${5}.${6}${7}${8}%v${11}", targetDB, targetDB)
			} else {
				if originTable.Query == "" {
					continue
				}
				return fmt.Errorf("error when try to replace database `%s` to `%s` in query: %s", originTable.Database, targetDB, originTable.Query)
			}
			originTable.Query = queryRE.ReplaceAllString(originTable.Query, substitution)
			originTable.Database = targetDB
			if len(uuidRE.FindAllString(originTable.Query, -1)) > 0 {
				newUUID, _ := uuid.NewUUID()
				substitution = fmt.Sprintf("UUID '%s'", newUUID.String())
				originTable.Query = uuidRE.ReplaceAllString(originTable.Query, substitution)
			}
			(*originTables)[i] = originTable
		}
	}
	return nil
}

func filterPartsByPartitionsFilter(tableMetadata metadata.TableMetadata, partitionsFilter common.EmptyMap) {
	if len(partitionsFilter) > 0 {
		for disk, parts := range tableMetadata.Parts {
			filteredParts := make([]metadata.Part, 0)
			for _, part := range parts {
				if filesystemhelper.IsPartInPartition(part.Name, partitionsFilter) {
					filteredParts = append(filteredParts, part)
				}
			}
			tableMetadata.Parts[disk] = filteredParts
		}
	}
}

func getTableListByPatternRemote(ctx context.Context, b *Backuper, remoteBackupMetadata *metadata.BackupMetadata, tablePattern string, dropTable bool) (ListOfTables, error) {
	result := ListOfTables{}
	tablePatterns := []string{"*"}

	if tablePattern != "" {
		tablePatterns = strings.Split(tablePattern, ",")
	}
	metadataPath := path.Join(remoteBackupMetadata.BackupName, "metadata")
	for _, t := range remoteBackupMetadata.Tables {
		if IsInformationSchema(t.Database) {
			continue
		}
		tableName := fmt.Sprintf("%s.%s", t.Database, t.Table)
		shallSkipped := false
		for _, skipPattern := range b.cfg.ClickHouse.SkipTables {
			if shallSkipped, _ = filepath.Match(skipPattern, tableName); shallSkipped {
				break
			}
		}
	tablePatterns:
		for _, p := range tablePatterns {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
				if matched, _ := filepath.Match(strings.Trim(p, " \t\r\n"), tableName); !matched || shallSkipped {
					continue
				}
				tmReader, err := b.dst.GetFileReader(ctx, path.Join(metadataPath, common.TablePathEncode(t.Database), fmt.Sprintf("%s.json", common.TablePathEncode(t.Table))))
				if err != nil {
					return nil, err
				}
				data, err := io.ReadAll(tmReader)
				if err != nil {
					return nil, err
				}
				err = tmReader.Close()
				if err != nil {
					return nil, err
				}
				var t metadata.TableMetadata
				if err = json.Unmarshal(data, &t); err != nil {
					return nil, err
				}
				result = addTableToListIfNotExistsOrEnrichQueryAndParts(result, t)
				break tablePatterns
			}
		}
	}
	result.Sort(dropTable)
	return result, nil
}

func getOrderByEngine(query string, dropTable bool) int64 {
	if strings.Contains(query, "ENGINE = Distributed") || strings.Contains(query, "ENGINE = Kafka") || strings.Contains(query, "ENGINE = RabbitMQ") {
		return 4
	}
	if strings.HasPrefix(query, "CREATE DICTIONARY") {
		return 3
	}
	if strings.HasPrefix(query, "CREATE VIEW") ||
		strings.HasPrefix(query, "CREATE LIVE VIEW") ||
		strings.HasPrefix(query, "CREATE WINDOW VIEW") ||
		strings.HasPrefix(query, "ATTACH WINDOW VIEW") ||
		strings.HasPrefix(query, "CREATE MATERIALIZED VIEW") ||
		strings.HasPrefix(query, "ATTACH MATERIALIZED VIEW") {
		if dropTable {
			return 1
		} else {
			return 2
		}
	}

	if strings.HasPrefix(query, "CREATE TABLE") &&
		(strings.Contains(query, ".inner_id.") || strings.Contains(query, ".inner.")) {
		if dropTable {
			return 2
		} else {
			return 1
		}
	}
	return 0
}

func parseTablePatternForDownload(tables []metadata.TableTitle, tablePattern string) []metadata.TableTitle {
	tablePatterns := []string{"*"}
	if tablePattern != "" {
		tablePatterns = strings.Split(tablePattern, ",")
	}
	var result []metadata.TableTitle
	for _, t := range tables {
		for _, pattern := range tablePatterns {
			tableName := fmt.Sprintf("%s.%s", t.Database, t.Table)
			if matched, _ := filepath.Match(strings.Trim(pattern, " \t\r\n"), tableName); matched {
				result = append(result, t)
				break
			}
		}
	}
	return result
}

func IsInformationSchema(database string) bool {
	for _, skipDatabase := range []string{"INFORMATION_SCHEMA", "information_schema", "_temporary_and_external_tables"} {
		if database == skipDatabase {
			return true
		}
	}
	return false
}
