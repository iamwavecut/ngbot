-- +migrate Up
DELETE FROM chat_members
WHERE NOT EXISTS (SELECT 1 FROM chats WHERE chats.id = chat_members.chat_id);

DELETE FROM chat_managers
WHERE NOT EXISTS (SELECT 1 FROM chats WHERE chats.id = chat_managers.chat_id);

DELETE FROM chat_bot_membership
WHERE NOT EXISTS (SELECT 1 FROM chats WHERE chats.id = chat_bot_membership.chat_id);

DELETE FROM admin_panel_sessions
WHERE NOT EXISTS (SELECT 1 FROM chats WHERE chats.id = admin_panel_sessions.chat_id);

DELETE FROM admin_panel_commands
WHERE NOT EXISTS (SELECT 1 FROM admin_panel_sessions WHERE admin_panel_sessions.id = admin_panel_commands.session_id);

DELETE FROM chat_spam_examples
WHERE NOT EXISTS (SELECT 1 FROM chats WHERE chats.id = chat_spam_examples.chat_id);

DELETE FROM spam_votes
WHERE NOT EXISTS (SELECT 1 FROM spam_cases WHERE spam_cases.id = spam_votes.case_id);

DELETE FROM spam_case_report_messages
WHERE NOT EXISTS (SELECT 1 FROM spam_cases WHERE spam_cases.id = spam_case_report_messages.case_id);

-- +migrate Down
SELECT 1;
