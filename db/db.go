package db

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	glc "git.lolli.tech/lollipopkit/go_lru_cacher"
	ndb "git.lolli.tech/lollipopkit/nano-db-sdk-go"
	"github.com/LollipopKit/custed-server/config"
	"github.com/LollipopKit/custed-server/consts"
	"github.com/LollipopKit/custed-server/logger"
	"github.com/LollipopKit/custed-server/model"
	"github.com/LollipopKit/custed-server/utils"
	jsoniter "github.com/json-iterator/go"
)

var (
	json = jsoniter.ConfigCompatibleWithStandardLibrary

	// DB
	DB = ndb.NewDB(config.DBUrl, consts.DBPwd)

	// map[string]TokenItem : {ID: token}
	tokenItemsCacher = glc.NewCacher(20000)

	// Errors
	ErrBadScheduleJson   = errors.New("bad schedule json")
	ErrTodayNoMoreLesson = errors.New("today have no more lesson")
	ErrBadSchedule       = errors.New("bad schedule object")
	ErrTokenCast         = errors.New("Token type cast error")
	ErrTokenItemCast     = errors.New("TokenItem type cast error")
	ErrNotFound          = errors.New("not found")
	ErrBadLessonTime     = errors.New("bad lesson time")
)

// 获取所有token
func GetTokenItems(force bool) ([]model.TokenItem, error) {
	// 如果本地有缓存且不强制刷新，则使用缓存
	tisLen := tokenItemsCacher.Len()
	if tisLen != 0 && !force {
		tis := make([]model.TokenItem, tisLen)
		for _, token := range tokenItemsCacher.Values() {
			t, ok := token.(model.TokenItem)
			if !ok {
				return tis, ErrTokenItemCast
			}
			tis = append(tis, t)
		}
		return tis, nil
	}

	// 否则从数据库获取
	ids, err := DB.Files("custed", "token")
	if err != nil {
		return []model.TokenItem{}, err
	}

	tis := make([]model.TokenItem, len(ids))
	ch := make(chan string, len(ids))
	for _, id := range ids {
		ch <- id
	}
	close(ch)

	// 并发
	wg := sync.WaitGroup{}
	wg.Add(consts.CoroutinesNum)
	for coCount := 0; coCount < consts.CoroutinesNum; coCount++ {
		go func() {
			for id := range ch {
				var token model.TokenItem
				err := DB.Read("custed/token/"+id, &token)
				if err != nil {
					logger.E("getTokenItems: read token error: %s", err)
					continue
				}
				tis = append(tis, token)
			}
			wg.Done()
		}()
	}
	wg.Wait()

	// 将在线获取的tokens放入缓存
	for _, token := range tis {
		tokenItemsCacher.Set(token.Id, token)
	}
	return tis, nil
}

// 获取单一对应token
func GetToken(id string, cache bool) (*model.TokenItem, error) {
	// 优先在本地缓存中查找
	t, ok := tokenItemsCacher.Get(id)
	if ok {
		tt, ok := t.(model.TokenItem)
		if !ok {
			return &tt, ErrTokenCast
		}
		return &tt, nil
	}

	// 本地无数据，从数据库中获取
	err := DB.Read("custed/token/"+id, &t)
	if err != nil {
		return nil, err
	}

	tokenItemsCacher.Set(id, t)

	tt, ok := t.(model.TokenItem)
	if !ok {
		return nil, ErrTokenItemCast
	}

	return &tt, nil
}

// 将现有token item与新数据合并
func combineTokens(tt model.TokenItem, token, ip, id string, platform int) model.TokenItem {
	havePlatform := false
	for idx, tok := range tt.Tokens {
		if tok.Platform == platform {
			tt.Tokens[idx].ID = token
			havePlatform = true
		}
	}

	if !havePlatform {
		tt.Tokens = append(tt.Tokens, model.Token{
			ID:       token,
			Platform: platform,
		})
	}

	if !utils.Contains(tt.IPs, ip) {
		tt.IPs = append(tt.IPs, ip)
	}
	return tt
}

// 更新token
func UpdateToken(token, ip, id string, platform int) error {
	var tt model.TokenItem

	// 先在内存查找
	t, ok := tokenItemsCacher.Get(id)
	if ok {
		tt, ok = t.(model.TokenItem)
		if !ok {
			return ErrTokenItemCast
		}
		tt = combineTokens(tt, token, ip, id, platform)
	} else {
		// 从数据库读
		err := DB.Read("custed/token/"+id, &tt)
		if err == nil {
			tt = combineTokens(tt, token, ip, id, platform)
		} else {
			// 如果不存在，则新建
			tt = model.TokenItem{
				Id: id,
				Tokens: []model.Token{
					{
						ID:       token,
						Platform: platform,
					},
				},
				CreateTime: time.Now().String(),
				// 将在后6行代码更新
				// LastTime:   time.Now().String(),
				IPs: []string{ip},
			}
		}
	}
	tt.LastTime = time.Now().String()

	tokenItemsCacher.Set(id, tt)
	return DB.Write("custed/token/"+id, tt)
}

// 更新课表
func UpdateSchedule(schedule model.JwSchedule, id string) error {
	return DB.Write("custed/schedule/"+id, schedule)
}

// 更新考试表
func UpdateExam(id string, exam model.JwExam) error {
	return DB.Write("custed/exam/"+id, exam)
}

// 更新成绩
func UpdateGrade(id string, grade model.JwGrade) error {
	return DB.Write("custed/grade/"+id, grade)
}

// 更新kbpro
func UpdateKBPro(id string, schedule model.JwKBPro) error {
	return DB.Write("custed/schedule-kbpro/"+id, schedule)
}

// 获取su的用户名列表
func GetSuperUserNames() ([]string, error) {
	var sus []string
	err := DB.Read("custed/data/"+consts.SuperUserListKey, &sus)
	if err != nil {
		return []string{}, err
	}

	return sus, nil
}

// 获取kbpro
func GetKBPro(id string) (model.JwKBPro, error) {
	var schedule model.JwKBPro
	err := DB.Read("custed/schedule-kbpro/"+id, &schedule)
	if err != nil {
		return nil, err
	}
	return schedule, nil
}

// 获取课表
func GetSchedule(id string) (model.JwSchedule, error) {
	var schedule model.JwSchedule
	err := DB.Read("custed/schedule/"+id, &schedule)
	if err != nil {
		return model.JwSchedule{}, err
	}
	return schedule, nil
}

// 获取成绩
func GetGrade(id string) (model.JwGrade, error) {
	var grade model.JwGrade
	err := DB.Read("custed/grade/"+id, &grade)
	if err != nil {
		return model.JwGrade{}, err
	}
	return grade, nil
}

// 获取考试
func GetExam(id string) (model.JwExam, error) {
	var exam model.JwExam
	err := DB.Read("custed/exam/"+id, &exam)
	if err != nil {
		return model.JwExam{}, err
	}
	return exam, nil
}

// 更新用户是否开启课程推送
func UpdateUsersEnableSchedulePush(users []string) error {
	return DB.Write("custed/data/"+consts.UsersEnableSchedulePushKey, users)
}

// 获取用户是否开启推送
func GetUsersEnableSchedulePush() (users []string, err error) {
	err = DB.Read("custed/data/"+consts.UsersEnableSchedulePushKey, &users)
	return
}

// 获取下一节课（桌面课表）（优先使用教务课表来源）
func GetNextLesson(id string, debug bool) (model.NextLesson, error) {
	now := time.Now()
	if config.CalculateWeeksOfSemester(now) < 0 {
		return model.NextLesson{}, ErrTodayNoMoreLesson
	}
	
	for idx := range consts.Holidays {
		h := &consts.Holidays[idx]
		if *&h.From.Year == now.Year() && *&h.From.Month == int(now.Month()) && *&h.From.Day == now.Day() {
			if consts.Holidays[idx].To == nil {
				return model.NextLesson{}, ErrTodayNoMoreLesson
			}
			now = time.Date(*&h.To.Year, time.Month(*&h.To.Month), *&h.To.Day, 0, 0, 0, 0, time.Local)
			break
		}
	}

	hour := now.Hour()
	minute := now.Minute()

	// 重复搜索的次数（搜索不是当天的课的次数）
	// 从-1开始，因为第一次搜索是当天的课
	searchAgainTimes := -1
	// 再次搜索课表
AGAIN:
	// 注释在: 常量[consts.NextScheduleSearchPassDays]
	if searchAgainTimes < consts.NextScheduleSearchPassDays && searchAgainTimes >= 0 {
		diffHours := 0

		if searchAgainTimes == 0 {
			// 补全当天剩余小时：diffHours = 当天剩余小时 + 1
			diffHours = 24 - hour + 1
		} else {
			// 每次循环，加一天
			diffHours = 24
		}

		// 因为搜索的不是当天的课，需要找到那一天的第一节课，所以需要把小时设置为0
		hour = 0
		minute = 0

		now = now.Add(time.Hour * time.Duration(diffHours))
	}

	var todayLessons model.LessonList
	var err error
	todayLessons, err = getNextLessonJwSchedule(id, now)
	if err != nil {
		todayLessons, err = getNextLessonKBPro(id, now)
		if err != nil {
			return model.NextLesson{}, err
		}
	}

	sort.Sort(todayLessons)
	//fmt.Printf("====\n%#v\n%#v\n====\n", now, todayLessons)
	var remain int
	for idx, lesson := range todayLessons {
		lessonSplit := strings.Split(lesson.StartTime, ":")
		if len(lessonSplit) != 2 {
			return model.NextLesson{}, ErrBadLessonTime
		}
		lessonHour, err := utils.Str2int(lessonSplit[0])
		if err != nil {
			return model.NextLesson{}, err
		}
		lessonMinute, err := utils.Str2int(lessonSplit[1])
		if err != nil {
			return model.NextLesson{}, err
		}
		todayLessons[idx].Teacher = strings.Trim(todayLessons[idx].Teacher, " ")
		if hour < lessonHour || (hour == lessonHour && minute < lessonMinute) {
			remain++
			continue
		}
	}

	remainCount := len(todayLessons)
	if remain == 0 {
		if searchAgainTimes >= consts.NextScheduleSearchPassDays-1 {
			return model.NextLesson{}, ErrTodayNoMoreLesson
		}
		searchAgainTimes += 1
		goto AGAIN
	}

	nextIdx := remainCount - remain

	nextLesson := todayLessons[nextIdx]
	switch searchAgainTimes {
	case 0:
		nextLesson.StartTime = "明天 " + nextLesson.StartTime
	case 1:
		nextLesson.StartTime = "后天 " + nextLesson.StartTime
	case 2:
		h := strings.Split(string(nextLesson.StartTime), ":")[0]
		h = strings.Replace(h, "0", "", 1)
		nextLesson.StartTime = "大后天 " + h + "点"
	}

	return nextLesson, nil
}

// 获取下一节课（桌面课表）（kbpro）
func getNextLessonKBPro(id string, now time.Time) ([]model.NextLesson, error) {
	schedule, err := GetKBPro(id)
	if err != nil {
		return []model.NextLesson{}, err
	}

	weekday := int(now.Weekday())
	nowWeek := config.CalculateWeeksOfSemester(now)

	var todayLessons model.LessonList

	for _, course := range schedule {
		if course.DayOfWeek != utils.Int2str(weekday) {
			continue
		}

		// 解析周数
		var weeks []int
		for idx, s := range course.WeekDescription {
			if s == rune('1') {
				weeks = append(weeks, idx)
			}
		}

		for idx := range weeks {
			if weeks[idx] == nowWeek {
				todayLessons = append(todayLessons, model.NextLesson{
					Name:      course.CourseName,
					Position:  course.BuildingName + course.ClassroomName,
					Teacher:   course.TeacherName,
					StartTime: utils.GetStartTimeBySection(course.BeginSection),
					Weeks:     weeks,
				})
			}
		}
	}

	return todayLessons, nil
}

// 获取下一节课（桌面课表）（教务课表）
func getNextLessonJwSchedule(id string, now time.Time) ([]model.NextLesson, error) {
	schedule, err := GetSchedule(id)
	if err != nil {
		return []model.NextLesson{}, err
	}

	if schedule.State != 0 {
		return []model.NextLesson{}, ErrBadScheduleJson
	}

	weekdayIdx := int(now.Weekday()) - 1
	if weekdayIdx == -1 {
		weekdayIdx = 6
	}

	nowWeek := config.CalculateWeeksOfSemester(now)

	if len(schedule.Data.AdjustDays) == 0 {
		return []model.NextLesson{}, ErrBadSchedule
	}

	todayData := schedule.Data.AdjustDays[weekdayIdx]
	var todayLessons model.LessonList
	var timePiecesShouldCheck []model.TimePiece

	timePiecesShouldCheck = append(timePiecesShouldCheck, todayData.AMTimePieces...)
	timePiecesShouldCheck = append(timePiecesShouldCheck, todayData.PMTimePieces...)
	timePiecesShouldCheck = append(timePiecesShouldCheck, todayData.EVTimePieces...)

	for _, item := range timePiecesShouldCheck {
		if len(item.Dtos) == 0 {
			continue
		}
		for _, lesson := range item.Dtos {
			var tempLesson model.NextLesson
			for _, content := range lesson.Content {
				if content.Key == "Time" {
					weeksString := strings.Split(content.Name, "周")
					for _, w := range strings.Split(weeksString[0], ",") {
						if strings.Contains(w, "-") {
							weekStartEnd := strings.Split(w, "-")
							start, err := utils.Str2int(weekStartEnd[0])
							if err != nil {
								return []model.NextLesson{}, err
							}
							end, err := utils.Str2int(weekStartEnd[1])
							if err != nil {
								return []model.NextLesson{}, err
							}
							isSingleWeekDay := strings.Contains(content.Name, "单周")
							isTwiceWeekDay := strings.Contains(content.Name, "双周")
							for i := start; i < end+1; i++ {
								if isSingleWeekDay {
									if i%2 == 1 {
										tempLesson.Weeks = append(tempLesson.Weeks, i)
									}
								} else if isTwiceWeekDay {
									if i%2 == 0 {
										tempLesson.Weeks = append(tempLesson.Weeks, i)
									}
								} else {
									tempLesson.Weeks = append(tempLesson.Weeks, i)
								}
							}
						} else {
							week, err := utils.Str2int(w)
							if err != nil {
								return []model.NextLesson{}, err
							}
							tempLesson.Weeks = append(tempLesson.Weeks, week)
						}
					}
				}
			}
			sort.Sort(tempLesson.Weeks)
			for _, weekIdx := range tempLesson.Weeks {
				if weekIdx == nowWeek {
					tempLesson.StartTime = item.StartTime
					for _, content := range lesson.Content {
						switch content.Key {
						case "Lesson":
							tempLesson.Name = content.Name
						case "Teacher":
							if !strings.Contains(tempLesson.Teacher, content.Name) {
								tempLesson.Teacher += fmt.Sprintf("%s ", content.Name)
							}
						case "Room":
							pos := content.Name
							pos = strings.Replace(pos, "[理论]", "[理]", 1)
							pos = strings.Replace(pos, "[实验]", "[实]", 1)
							tempLesson.Position = pos
						}
					}
					todayLessons = append(todayLessons, tempLesson)
					break
				}
			}
		}
	}

	return todayLessons, nil
}
