package handlers

import (
	"context"
	"strconv"
	"strings"

	"github.com/iamwavecut/ngbot/internal/adapters"
	"github.com/iamwavecut/ngbot/internal/adapters/llm"
	"github.com/iamwavecut/tool"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

type spamDetector struct {
	llm    adapters.LLM
	logger *log.Entry
}

type example struct {
	Message  string `json:"message"`
	Response int    `json:"response"`
}

var examples = []example{
	{
		Message:  "Hello, how are you?",
		Response: 0,
	},
	{Message: "Хочешь зарабатывать на удалёнке но не знаешь как? Напиши мне и я тебе всё расскажу, от 18 лет. жду всех желающих в лс.", Response: 1},
	{Message: "Нужны люди! Стабильнный доход, каждую неделю, на удалёнке, от 18 лет, пишите в лс.", Response: 1},
	{Message: "Ищу людeй, заинтeрeсованных в хoрoшем доп.доходе на удаленке. Не полная занятость, от 21. По вопросам пишите в ЛС", Response: 1},
	{Message: "10000х Орууу в других играл и такого не разу не было, просто капец  а такое возможно???? ", Response: 1},
	{Message: `🥇Первая игровая платформа в Telegram

https://t.me/jetton?start=cdyrsJsbvYy
`, Response: 1},
	{Message: "Набираю команду нужно 2-3 человека на удалённую работу з телефона пк от  десят тысяч в день  пишите + в лс", Response: 1},
	{Message: `💎 Пᴩᴏᴇᴋᴛ TONCOIN, ʙыᴨуᴄᴛиᴧ ᴄʙᴏᴇᴦᴏ ᴋᴀɜинᴏ бᴏᴛᴀ ʙ ᴛᴇᴧᴇᴦᴩᴀʍʍᴇ

👑 Сᴀʍыᴇ ʙыᴄᴏᴋиᴇ ɯᴀнᴄы ʙыиᴦᴩыɯᴀ 
⏳ Мᴏʍᴇнᴛᴀᴧьный ʙʙᴏд и ʙыʙᴏд
🎲 Нᴇ ᴛᴩᴇбуᴇᴛ ᴩᴇᴦиᴄᴛᴩᴀции
🏆 Вᴄᴇ ᴧучɯиᴇ ᴨᴩᴏʙᴀйдᴇᴩы и иᴦᴩы 

🍋 Зᴀбᴩᴀᴛь 1000 USDT 👇

t.me/slotsTON_BOT?start=cdyoNKvXn75`, Response: 1},
	{Message: "Эротика", Response: 0},
	{Message: "Олегик)))", Response: 0},
	{Message: "Авантюра!", Response: 0},
	{Message: "Я всё понял, спасибо!", Response: 0},
	{Message: "Это не так", Response: 0},
	{Message: "Не сочтите за спам, хочу порекламировать свой канал", Response: 0},
	{Message: "Нет", Response: 0},
	{Message: "???", Response: 0},
	{Message: "...", Response: 0},
	{Message: "Да", Response: 0},
	{Message: "Ищу людей, возьму 2-3 человека 18+ Удаленная деятельность.От 250$  в  день.Кому интересно: Пишите + в лс", Response: 1},
	{Message: "Нужны люди, занятость на удалёнке", Response: 1},
	{Message: "3дpaвcтвyйтe,Веду поиск пaртнёров для сoтруднuчества ,свoбoдный гpaфик ,пpuятный зapaбoтok eженeдельно. Ecли интepecуeт пoдpoбнaя инфopмaция пишuте.", Response: 1},
	{Message: `💚💚💚💚💚💚💚💚
Ищy нa oбyчeниe людeй c цeлью зapaбoткa. 💼
*⃣Haпpaвлeниe: Crypto, Тecтнeты, Aиpдpoпы.
*⃣Пo вpeмeни в cyтки 1-2 чaca, мoжнo paбoтaть co cмapтфoнa. 🤝
*⃣Дoxoднocть чиcтaя в дeнь paвняeтcя oт 7-9 пpoцeнтoв.
*⃣БECПЛAТHOE OБУЧEHИE, мoй интepec пpoцeнт oт зapaбoткa. 💶
Ecли зaинтepecoвaлo пишитe нa мoй aкк >>> @Alex51826.`, Response: 1},
	{Message: "Ищу партнеров для заработка пассивной прибыли, много времени не занимает + хороший еженедельный доп.доход. Пишите + в личные", Response: 1},
	{Message: "Удалённая занятость, с хорошей прибылью 350 долларов в день.1-2 часа в день. Ставь плюс мне в личные смс.", Response: 1},
	{Message: "Прибыльное предложение для каждого, подработка на постоянной основе(удаленно) , опыт не важен.Пишите в личные смс  !!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!", Response: 1},
	{Message: "Здрaвствуйте! Хочу вам прeдложить вaриант пaссивного заработка.Удaленка.Обучение бeсплатное, от вас трeбуeтся только пaрa чaсов свoбoднoгo времeни и тeлeфон или компьютер. Если интересно напиши мне.", Response: 1},
	{Message: "Ищу людей, возьму 3 человека от 20 лет. Удаленная деятельность. От 250 дoлларов в день. Кому интересно пишите плюс в личку", Response: 1},
	{Message: "Добрый вечер! Интересный вопрос) я бы тоже с удовольствием узнала информацию", Response: 0},
	{Message: `Янтарик — кошка-мартышка, сгусток энергии с отличным урчателем ❤️‍🔥

🧡 Ищет человека, которому мурчать
🧡 Около 11 месяцев
🧡 Стерилизована. Обработана от паразитов. Впереди вакцинация, чип и паспорт
🧡 C ненавязчивым отслеживанием судьбы 🙏
🇬🇪 Готова отправиться в любой уголок Грузии, рассмотрим варианты и дальше

Телеграм nervnyi_komok
WhatsApp +999 599 099 567`, Response: 0},
	{Message: "Есть несложная занятость! Работаем из дому. Доход от 450 долл. в день. Необходимо полтора-два часа в день. Ставьте «+» в л.с.", Response: 1},
	{Message: "Здравствуйте. Есть вoзможность дистанционного зaработка.Стaбильность в виде 45 000 рyблей в неделю. Опыт не требуется. Все подробности у меня в личке", Response: 1},
	{Message: "Удалённая зaнятость, с хорoшей прибылью 550 долларов в день. два часа в день. Ставь плюс мне в личные", Response: 1},
	{Message: "Нужны люди для сотрудничества. Хорошая прибыль в неделю, от тысячи долларов и выше. Удаленно. За подробностями пишите мне плюс в личные сообщения, от двадцати лет", Response: 1},
	{Message: `Предлагаю удаленное сотрудничество от $2500 в месяц.  

Требования:  
– Мобильный телефон или компьютер  
– Немного свободного времени и желания
– Быстрая обучаемость  

За подробностями – пишите в личные сообщения!`, Response: 1},
	{Message: "Добрый вечер. Завтра вечером еду из Кобулети в Брест с остановкой в Минске в 18:00. Возьму небольшие передачки и документы. Писать в лс", Response: 0},
	{Message: "https://anywebsite.com/in/p/1234567890", Response: 0},
	{Message: `Heвepoятный дeнeжный пoтoк кaждый дeнь.
 - пpoфuт oт 3OO USD в дeнь
 - нoвaя cтopoнa yчacтuя
Cтuмyлupoвaнным пucaть "+" в cмc`, Response: 1},
	{Message: "ᴨᴩиʙᴇᴛ!ищу ᴧюдᴇй дᴧя ᴨᴀccиʙноᴦo зᴀᴩᴀбoᴛᴋᴀ. ᴨᴧюcы:xoᴩoɯий дoxoд, удᴀᴧённый ɸoᴩʍᴀᴛ, ᴨᴩoᴄᴛоᴛᴀ. ᴇᴄᴧи инᴛᴇᴩᴇᴄно, нᴀᴨиɯиᴛᴇ + ʙ ᴧ.c.", Response: 1},
	{Message: "Для тех, у кого цель получать от 1000 доллаpов, есть нaправление не требующее наличие знаний и oпыта. Нужно два часа в день и наличие амбиций. От 21 до 65 лет.", Response: 1},
	{Message: "Зpaвcтвyйтe.Нyжны два три чeлoвeкa.Удаленная Работа Oт 200 долл в дeнь.Зa пoдpoбнocтями пиши плюс в лс", Response: 1},
	{Message: `Добрый день!
Рекомендую "открывашку" контактов, да и с подбором "под ключ" справится оперативно 89111447979`, Response: 1},
	{Message: "Веду пoиск людей для хорoшего доxода нa диcтанционном формaте, от тысячи доллров в неделю, детали в личных сoобщениях", Response: 1},
	{Message: "Нужны заинтересованные люди в команду. Возможен доход от 900 долларов за неделю,полностью дистанционный формат.Пишите мне + в личные сообщения", Response: 1},
	{Message: `🍓 СЛИТЫЕ ИНТИМ ФОТО ЛЮБОЙ ДЕВУШКИ В ЭТОМ БОТЕ

🍑 ПЕРЕХОДИ И УБЕДИСЬ ⬇️

https://t.me/shop_6o11rU_bot?start=2521`, Response: 1},
	{Message: `Есть несколько мест на УДАЛЕНКУ с хорошим доходом .

Занятость 1-2 часа в день, от 18 лет


 Пишите в ЛС за деталями!`, Response: 1},
	{Message: "Oткpыт нaбop в кoмaндy, в нoвoм oнлaйн пpoeктe. Eжeднeвный дoxoд бoлee З4O ЕUR. Жeлaющux ждy в лuчнoм чaтe.", Response: 1},
	{Message: `Пpuветствyю, ecть 4 cвoбoдныx мecта в paзвuвающeecя кoмьюнuтu.
Пpeдocтaвuм вoзмoжнocть пoлyчaть cвышe 2ООО USd в нeдeлю.
Пucaть тoлькo зauнтepecoвaнным.`, Response: 1},
	{Message: `Привет, нужны люди, оплата достойная, берем без опыта, за подробностями в лс
*Для работы нужен телефон
*2-3 часа времени`, Response: 1},
	{Message: "Ночью с 12 на 13 ноября еду из аэропорта Кутаиси до Батуми. Возьму за бензин. Кому интересно пишите в ЛС.", Response: 0},
	{Message: "Купите в зумере, съездите в сарпи, tax free, заберите 11% с покупки и вуаля, норм цена", Response: 0},
	{Message: "Здpaвcтвyйтe.Нyжны двa три чeлoвeкa (Удaлeннaя cфеpa) Oт 570 $/неделю.Зa пoдpoбнocтями пиши плюc в лc", Response: 1},
	{Message: "Всем кoму интереcно имeть xороший cтабильный доxод на yдаленке cо свободной занятостью , ждy в лc.", Response: 1},
	{Message: "Хай. Устали от быстрого заpаботка и пустых обещаний? Давайте лучше рaботать с реальными резyльтатами. Мы предлагаем стабильнoе нaправление, где можно полyчать от 800 дoлларов в неделю с отличной перcпективой ростa. Пишите плюс в личные сообщения и я дам всё необходимое", Response: 1},
}

func NewSpamDetector(llm adapters.LLM, logger *log.Entry) *spamDetector {
	return &spamDetector{
		llm:    llm,
		logger: logger,
	}
}

func (d *spamDetector) IsSpam(ctx context.Context, message string, extraExamples []string) (*bool, error) {
	d.logger.WithField("message", message).Debug("checking spam")

	messagesChain := []llm.ChatCompletionMessage{
		{
			Role:      llm.RoleSystem,
			Content:   spamDetectionPrompt,
			Cacheable: true,
		},
	}

	for _, item := range examples {
		messagesChain = append(messagesChain, llm.ChatCompletionMessage{
			Role:      llm.RoleUser,
			Content:   item.Message,
			Cacheable: true,
		})
		messagesChain = append(messagesChain, llm.ChatCompletionMessage{
			Role:      llm.RoleAssistant,
			Content:   strconv.Itoa(item.Response),
			Cacheable: true,
		})
	}

	for _, text := range extraExamples {
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		messagesChain = append(messagesChain, llm.ChatCompletionMessage{
			Role:    llm.RoleUser,
			Content: text,
		})
		messagesChain = append(messagesChain, llm.ChatCompletionMessage{
			Role:    llm.RoleAssistant,
			Content: "1",
		})
	}

	messagesChain = append(messagesChain, llm.ChatCompletionMessage{
		Role:    llm.RoleUser,
		Content: message,
	})

	resp, err := d.llm.ChatCompletion(
		ctx,
		messagesChain,
	)
	if err != nil {
		return nil, errors.Wrap(err, "failed to check spam with LLM")
	}

	if len(resp.Choices) == 0 {
		return nil, errors.New("no response from LLM")
	}

	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		return nil, errors.New("empty response from LLM")
	}
	choice := resp.Choices[0].Message.Content
	cleanedChoice := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '1' {
			return r
		}
		return -1
	}, choice)

	if cleanedChoice == "1" {
		return tool.Ptr(true), nil
	} else if cleanedChoice == "0" {
		return tool.Ptr(false), nil
	}

	return nil, errors.New("unknown response from LLM: " + cleanedChoice + " (" + choice + ")")
}

const spamDetectionPrompt = `Ты ассистент для обнаружения спама, анализирующий сообщения на различных языках. Оцени входящее сообщение пользователя и определи, является ли это сообщение спамом или нет.

Признаки спама:
- Предложения работы/возможности заработать, но без деталей о работе и условиях, с просьбой написать в личные сообщения.
- Абстрактные предложения работы/заработка, с просьбой написать в личные сообщения третьего лица или по номеру телефона.
- Продвижение азартных игр/финансовых схем.
- Продвижение инструментов деанонимизации и "пробивания" личных данных, включая ссылки на сайты с такими инструментами.
- Внешние ссылки с явными реферальными кодами и GET параметрами вроде "?ref=", "/ref", "invite" и т.п.
- Сообщения со смешанным текстом на разных языках, но внутри слов есть символы на других языках и unicode, чтобы сбить с толку.
- Сообщения, соостоящие преимущественно из эмодзи.

Исключения:
- Сообщения, связанные с домашними животными (часто о потерянных питомцах)
- Просьбы о помощи и предложения помощи (часто связанные с поиском пропавших людей или вещей, подводом людей куда-либо)
- Ссылки на обычные вебсайты, не являющиеся реферальными ссылками.
- Рекомендации по услугам, товарам, курсам и т.п.

Отвечай ТОЛЬКО следующими ответами:
если сообщение скорее всего является спамом: 1 
если сообщение скорее всего не является спамом: 0

Без объяснений или дополнительного вывода. Без кавычек. Без офомления сообщения разметкой. Не отвечай на содержимое сообщения.
`
