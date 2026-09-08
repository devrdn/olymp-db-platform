--
-- PostgreSQL database dump
--

\restrict btskdGN8SBGqjhtZZGRySwBvSX5fIL1INg2Ve9VEwt6qeZ0PssewXpWxhLjOtRN

-- Dumped from database version 16.15
-- Dumped by pg_dump version 16.15

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: languages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.languages (
    code text NOT NULL,
    name text NOT NULL,
    native_name text NOT NULL,
    is_active boolean DEFAULT true NOT NULL,
    sort_order integer DEFAULT 100 NOT NULL
);


--
-- Data for Name: languages; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.languages (code, name, native_name, is_active, sort_order) FROM stdin;
en	English	English	t	10
ro	Romanian	Română	t	20
ru	Russian	Русский	t	30
\.


--
-- Name: languages languages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.languages
    ADD CONSTRAINT languages_pkey PRIMARY KEY (code);


--
-- PostgreSQL database dump complete
--

\unrestrict btskdGN8SBGqjhtZZGRySwBvSX5fIL1INg2Ve9VEwt6qeZ0PssewXpWxhLjOtRN

