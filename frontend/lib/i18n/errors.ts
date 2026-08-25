import type { ApiError } from "@/lib/api/client";

/**
 * Error text lives here, keyed by the machine code the API returns.
 *
 * The server sends an English `message` alongside the code, but it is a
 * developer aid: it is not translated, not reviewed as product copy, and may
 * name internals. The interface never renders it (spec section 8).
 */
const MESSAGES: Record<string, string> = {
  // Authentication and the account itself
  invalid_credentials: "Неверный логин или пароль.",
  unauthenticated: "Сессия истекла. Войдите заново.",
  account_blocked: "Учётная запись заблокирована. Обратитесь к администратору.",
  password_change_required: "Смените временный пароль, чтобы продолжить.",
  too_many_attempts: "Слишком много попыток. Подождите и попробуйте снова.",
  wrong_password: "Текущий пароль указан неверно.",
  invalid_password: "Пароль не подходит под требования.",
  weak_password: "Пароль слишком простой. Возьмите длиннее и разнообразнее.",
  same_password: "Новый пароль совпадает со старым.",
  login_taken: "Такой логин уже занят.",
  email_taken: "Такая почта уже используется.",

  // Rights and addressing
  forbidden: "Недостаточно прав для этого действия.",
  cannot_act_on_self: "Это действие нельзя применить к себе.",
  owner_immutable: "Владельца олимпиады снять нельзя.",
  address_not_allowed: "Доступ разрешён только из сети университета.",

  // Contest lifecycle
  invalid_transition: "Из текущего состояния такой переход невозможен.",
  not_editable: "Олимпиаду в этом состоянии нельзя менять.",
  enrollment_closed: "Запись на эту олимпиаду закрыта.",
  already_enrolled: "Вы уже записаны на эту олимпиаду.",
  participant_started: "Участник уже начал — его нельзя удалить, только дисквалифицировать.",

  // Things that were not found
  not_found: "Не найдено.",
  user_not_found: "Пользователь не найден.",
  manager_not_found: "Менеджер не найден.",
  participant_not_found: "Участник не найден.",
  question_not_found: "Вопрос не найден.",
  story_not_found: "История ещё не задана.",

  // Malformed input
  invalid_request: "Проверьте заполненные поля.",
  invalid_cidr: "Неверный формат диапазона IP. Пример: 10.24.0.0/16",
  invalid_contest_id: "Неверный идентификатор олимпиады.",
  invalid_question_id: "Неверный идентификатор вопроса.",
  invalid_user_id: "Неверный идентификатор пользователя.",

  // Transport and the server itself
  method_not_allowed: "Действие недоступно для этого ресурса.",
  internal_error: "Сервер не смог обработать запрос.",
  unreachable: "Сервер недоступен. Проверьте соединение.",
};

const FALLBACK = "Что-то пошло не так. Попробуйте ещё раз.";

/**
 * A code absent from the dictionary is a deploy where the API grew a code the
 * interface has not learned yet. Showing the untranslated English would be
 * worse than a plain sentence, so the fallback is deliberate, not defensive.
 */
export function messageForError(failure: ApiError): string {
  return MESSAGES[failure.code] ?? FALLBACK;
}

/** Exposed so a test can assert the dictionary covers the server's vocabulary. */
export function knownErrorCodes(): string[] {
  return Object.keys(MESSAGES);
}
